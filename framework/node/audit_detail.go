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
	"fmt"
	"strings"

	"go.openpit.dev/officer/framework/domain"
)

// setRateLimitDetail renders a rate-limit upsert as a short human-readable audit
// detail, e.g. "set limit rate_limit account=acc-1 max_orders=100 window=1s".
func setRateLimitDetail(limit domain.LimitRate) string {
	var b strings.Builder
	b.WriteString("set limit ")
	b.WriteString(axesDetail(LimitTarget{
		Policy:  domain.PolicyRateLimit,
		Scope:   limit.Scope,
		Account: limit.Account,
		Asset:   limit.Asset,
	}))
	fmt.Fprintf(&b, " max_orders=%d window=%s", limit.MaxOrders, limit.Window)
	return b.String()
}

// setOrderSizeLimitDetail renders an order-size upsert as a short audit detail.
func setOrderSizeLimitDetail(limit domain.LimitOrderSize) string {
	var b strings.Builder
	b.WriteString("set limit ")
	b.WriteString(axesDetail(LimitTarget{
		Policy:  domain.PolicyOrderSizeLimit,
		Scope:   limit.Scope,
		Account: limit.Account,
		Asset:   limit.Asset,
	}))
	if limit.MaxQuantity != "" {
		b.WriteString(" max_quantity=")
		b.WriteString(limit.MaxQuantity)
	}
	if limit.MaxNotional != "" {
		b.WriteString(" max_notional=")
		b.WriteString(limit.MaxNotional)
	}
	return b.String()
}

// setSpotFundsPnlBoundsLimitDetail renders a SpotFunds P&L-bounds upsert.
func setSpotFundsPnlBoundsLimitDetail(limit domain.LimitSpotFundsPnlBounds) string {
	var b strings.Builder
	b.WriteString("set limit ")
	b.WriteString(axesDetail(LimitTarget{
		Policy:       domain.PolicySpotFundsPnlBoundsKillSwitch,
		Scope:        limit.Scope,
		Account:      limit.Account,
		AccountGroup: limit.AccountGroup,
	}))
	if limit.LowerBound != "" {
		b.WriteString(" lower_bound=")
		b.WriteString(limit.LowerBound)
	}
	if limit.UpperBound != "" {
		b.WriteString(" upper_bound=")
		b.WriteString(limit.UpperBound)
	}
	return b.String()
}

// deleteLimitDetail renders a barrier deletion as a short audit detail.
func deleteLimitDetail(target LimitTarget) string {
	return "delete limit " + axesDetail(target)
}

// setAccountGroupDetail renders an account group change. The move is filed under
// each non-empty side, so the line names both: a reader who found the row under
// the origin group and one who found it under the destination group must both
// see where the account came from and where it went.
func setAccountGroupDetail(id domain.AccountID, prevGroupCode, groupCode string) string {
	return fmt.Sprintf(
		"set group account %s from=%s to=%s",
		id, groupSideDetail(prevGroupCode), groupSideDetail(groupCode),
	)
}

// updateAccountDetail renders an account update. A renamed account is filed
// under both codes, so the line names both; a title-only update has no
// transition to state.
func updateAccountDetail(prevCode, code domain.AccountID) string {
	if prevCode == code {
		return fmt.Sprintf("update account %s", code)
	}
	return fmt.Sprintf("update account %s -> %s", prevCode, code)
}

// updateGroupDetail renders a group update. A renamed group is filed under both
// codes, so the line names both; a title-only update has no transition to state.
func updateGroupDetail(prevCode, code string) string {
	if prevCode == code {
		return fmt.Sprintf("update group %s", code)
	}
	return fmt.Sprintf("update group %s -> %s", prevCode, code)
}

// groupSideDetail renders one side of a group transition. The empty code is the
// reserved default group, which has no operator-facing code to name.
func groupSideDetail(code string) string {
	if code == "" {
		return "<none>"
	}
	return code
}

// adjustmentDetail renders one spot-funds adjustment and its accept/reject
// disposition.
func adjustmentDetail(id domain.AccountID, asset string, accepted bool) string {
	disposition := "rejected"
	if accepted {
		disposition = "accepted"
	}
	return fmt.Sprintf("adjustment account %s asset=%s %s", id, asset, disposition)
}

// setMcpAccessDetail renders one MCP command access toggle.
func setMcpAccessDetail(command string, enabled bool) string {
	state := "disable"
	if enabled {
		state = "enable"
	}
	return fmt.Sprintf("%s mcp command %s", state, command)
}

// marketDataToggleDetail renders one market-data enable/disable toggle.
func marketDataToggleDetail(kind, id string, enabled bool) string {
	state := "disable"
	if enabled {
		state = "enable"
	}
	return fmt.Sprintf("%s market-data %s %s", state, kind, id)
}

// submitOrderDetail renders one order submission and its accept/reject
// disposition. The order is named by its opaque external id.
func submitOrderDetail(order domain.Order, accepted bool) string {
	disposition := "rejected"
	if accepted {
		disposition = "committed"
	}
	return fmt.Sprintf("submit order %s account %s %s/%s %s %s",
		order.ExternalID, order.Account, order.BaseAsset, order.QuoteAsset, order.Side, disposition)
}

// executionReportDetail renders one execution report and how many account
// blocks it produced.
func executionReportDetail(
	in domain.ExecutionReportInput, status domain.OrderStatus, blocks int,
) string {
	return fmt.Sprintf("execution report order %s account %s %s/%s qty=%s %s blocks=%d",
		in.Order, in.Account, in.BaseAsset, in.QuoteAsset, in.FillQuantity, status, blocks)
}

// blockDetail renders one operator-initiated account block. The reason is the
// only record of why the operator acted: account.block_reason is overwritten in
// place and cleared on unblock, so after block-unblock-block the first reason
// survives nowhere else.
func blockDetail(id domain.AccountID, reason string) string {
	return blockReasonDetail("block account", id.String(), reason)
}

// unblockDetail renders one operator-initiated account unblock. reason must be
// empty: an unblock is never justified by the reason of the block it lifts, and
// rendering that text would read as the operator's justification for unblocking.
func unblockDetail(id domain.AccountID, reason string) string {
	return blockReasonDetail("unblock account", id.String(), reason)
}

// blockGroupDetail renders one operator-initiated group block.
func blockGroupDetail(code, reason string) string {
	return blockReasonDetail("block group", code, reason)
}

// unblockGroupDetail renders one operator-initiated group unblock. reason
// follows unblockDetail and must be empty.
func unblockGroupDetail(code, reason string) string {
	return blockReasonDetail("unblock group", code, reason)
}

// blockReasonDetail appends the operator's reason to a bare block detail line,
// falling back to the bare form when no reason was given. The reason is
// validated as printable upstream, so it cannot forge an extra audit line here.
func blockReasonDetail(operation, code, reason string) string {
	if reason == "" {
		return fmt.Sprintf("%s %s", operation, code)
	}
	return fmt.Sprintf("%s %s: %s", operation, code, reason)
}

// engineBlockDetail renders one engine-initiated (kill-switch) account block,
// naming the triggering order by its external id and the engine's stable reject
// code and reason so an operator can see why the account was blocked.
func engineBlockDetail(order domain.ExternalID, block domain.ExecutionAccountBlock) string {
	detail := fmt.Sprintf("engine blocked account %s order %s code=%s: %s",
		block.Account, order, block.Code, block.Reason)
	if block.Details != "" {
		detail += " (" + block.Details + ")"
	}
	return detail
}

// policyConfigurationBlockDetail identifies an engine block that arose while
// applying a live policy update, before any subsequent account work can run.
func policyConfigurationBlockDetail(policy string, block domain.AccountBlock) string {
	blockPolicy := block.Policy
	if blockPolicy == "" {
		blockPolicy = policy
	}
	detail := fmt.Sprintf("engine blocked account %s policy %s code=%s: %s",
		block.Account, blockPolicy, block.Code, block.Reason)
	if block.Details != "" {
		detail += " (" + block.Details + ")"
	}
	return detail
}

// axesDetail renders the policy/scope axes of a typed barrier.
func axesDetail(target LimitTarget) string {
	var b strings.Builder
	b.WriteString(target.Policy)
	b.WriteByte(' ')
	b.WriteString(target.Scope)
	if target.Account != "" {
		b.WriteString(" account=")
		b.WriteString(target.Account.String())
	}
	if target.AccountGroup != "" {
		b.WriteString(" account_group=")
		b.WriteString(target.AccountGroup)
	}
	if target.Asset != "" {
		b.WriteString(" asset=")
		b.WriteString(target.Asset)
	}
	return b.String()
}
