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
	b.WriteString(axesDetail(domain.PolicyRateLimit, limit.Scope, limit.Account, limit.Asset))
	fmt.Fprintf(&b, " max_orders=%d window=%s", limit.MaxOrders, limit.Window)
	return b.String()
}

// setOrderSizeLimitDetail renders an order-size upsert as a short audit detail.
func setOrderSizeLimitDetail(limit domain.LimitOrderSize) string {
	var b strings.Builder
	b.WriteString("set limit ")
	b.WriteString(axesDetail(domain.PolicyOrderSizeLimit, limit.Scope, limit.Account, limit.Asset))
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

// setPnlBoundsLimitDetail renders a P&L-bounds upsert as a short audit detail.
func setPnlBoundsLimitDetail(limit domain.LimitPnlBounds) string {
	var b strings.Builder
	b.WriteString("set limit ")
	b.WriteString(axesDetail(domain.PolicyPnlBoundsKillSwitch, limit.Scope, limit.Account, limit.Asset))
	if limit.LowerBound != "" {
		b.WriteString(" lower_bound=")
		b.WriteString(limit.LowerBound)
	}
	if limit.UpperBound != "" {
		b.WriteString(" upper_bound=")
		b.WriteString(limit.UpperBound)
	}
	if limit.InitialPnl != "" {
		b.WriteString(" initial_pnl=")
		b.WriteString(limit.InitialPnl)
	}
	return b.String()
}

// deleteLimitDetail renders a barrier deletion as a short audit detail.
func deleteLimitDetail(target LimitTarget) string {
	return "delete limit " + axesDetail(target.Policy, target.Scope, target.Account, target.Asset)
}

// setAccountGroupDetail renders an account group change; an empty groupCode is a
// clear.
func setAccountGroupDetail(id domain.AccountID, groupCode string) string {
	if groupCode == "" {
		return fmt.Sprintf("clear group account %s", id)
	}
	return fmt.Sprintf("set group account %s group=%s", id, groupCode)
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

// importPositionSnapshotDetail renders the internal snapshot-import adjustment
// with all persisted position values that do not fit in the public adjustment
// request.
func importPositionSnapshotDetail(snapshot domain.Balance, accepted bool) string {
	disposition := "rejected"
	if accepted {
		disposition = "accepted"
	}
	return fmt.Sprintf(
		"import position snapshot account %s asset=%s available=%s held=%s incoming=%s realized_pnl=%s average_entry_price=%s %s",
		snapshot.Account,
		snapshot.Asset,
		snapshot.Available,
		snapshot.Held,
		snapshot.Incoming,
		snapshot.RealizedPnl,
		snapshot.AverageEntryPrice,
		disposition,
	)
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

// executionReportDetail renders one execution report, its fill, and how many
// account blocks it produced.
func executionReportDetail(in domain.ExecutionReportInput, blocks int) string {
	status := "partially_filled"
	if in.Final {
		status = "filled"
	}
	return fmt.Sprintf("execution report order %s account %s %s/%s qty=%s %s blocks=%d",
		in.Order, in.Account, in.BaseAsset, in.QuoteAsset, in.FillQuantity, status, blocks)
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

// engineBlockReason composes the block_reason persisted on the account for an
// engine-initiated (kill-switch) block: the engine's human reason plus the
// cause (stable reject code and triggering order). Persisting the cause on the
// account itself - not only in the audit log - lets the Accounts surface show
// what blocked the account and why.
func engineBlockReason(order domain.ExternalID, block domain.ExecutionAccountBlock) string {
	reason := block.Reason
	if reason == "" {
		reason = block.Code
	}
	cause := fmt.Sprintf("code=%s, order %s", block.Code, order)
	if block.Details != "" {
		cause += ", " + block.Details
	}
	return fmt.Sprintf("%s [%s]", reason, cause)
}

// axesDetail renders the policy/scope/account/asset axes of a typed barrier.
func axesDetail(policy string, scope domain.LimitScope, account domain.AccountID, asset string) string {
	var b strings.Builder
	b.WriteString(policy)
	b.WriteByte(' ')
	b.WriteString(scope)
	if account != "" {
		b.WriteString(" account=")
		b.WriteString(account.String())
	}
	if asset != "" {
		b.WriteString(" asset=")
		b.WriteString(asset)
	}
	return b.String()
}
