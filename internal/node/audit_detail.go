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

	"go.openpit.dev/officer/internal/domain"
)

// setLimitDetail renders a barrier upsert as a short human-readable audit
// detail, e.g. "set limit rate_limit asset=AAPL max_orders=100 window=1s".
// Values is already sorted by kind.
func setLimitDetail(limit domain.Limit) string {
	var b strings.Builder
	b.WriteString("set limit ")
	b.WriteString(targetDetail(limit.Target))
	for _, v := range limit.Values {
		b.WriteByte(' ')
		b.WriteString(v.Kind)
		b.WriteByte('=')
		b.WriteString(v.Value)
	}
	return b.String()
}

// deleteLimitDetail renders a barrier deletion as a short audit detail.
func deleteLimitDetail(target domain.LimitTarget) string {
	return "delete limit " + targetDetail(target)
}

// setAccountGroupDetail renders an account group change; an empty groupID is a
// clear.
func setAccountGroupDetail(id domain.AccountID, groupID string) string {
	if groupID == "" {
		return fmt.Sprintf("clear group account %s", id)
	}
	return fmt.Sprintf("set group account %s group=%s", id, groupID)
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
// disposition.
func submitOrderDetail(order domain.Order, accepted bool) string {
	disposition := "rejected"
	if accepted {
		disposition = "committed"
	}
	return fmt.Sprintf("submit order %d account %s %s/%s %s %s",
		order.ID, order.Account, order.BaseAsset, order.QuoteAsset, order.Side, disposition)
}

// executionReportDetail renders one execution report, its fill, and how many
// account blocks it produced.
func executionReportDetail(in domain.ExecutionReportInput, blocks int) string {
	status := "partially_filled"
	if in.Final {
		status = "filled"
	}
	return fmt.Sprintf("execution report order %d account %s %s/%s qty=%s %s blocks=%d",
		in.OrderID, in.Account, in.BaseAsset, in.QuoteAsset, in.FillQuantity, status, blocks)
}

// engineBlockDetail renders one engine-initiated (kill-switch) account block,
// naming the triggering order and the engine's stable reject code and reason so
// an operator can see why the account was blocked.
func engineBlockDetail(orderID int64, block domain.ExecutionAccountBlock) string {
	detail := fmt.Sprintf("engine blocked account %s order %d code=%s: %s",
		block.Account, orderID, block.Code, block.Reason)
	if block.Details != "" {
		detail += " (" + block.Details + ")"
	}
	return detail
}

// targetDetail renders the policy/scope/account/asset axes of a target.
func targetDetail(target domain.LimitTarget) string {
	var b strings.Builder
	b.WriteString(target.Policy)
	b.WriteByte(' ')
	b.WriteString(target.Scope)
	if target.Account != "" {
		b.WriteString(" account=")
		b.WriteString(target.Account.String())
	}
	if target.Asset != "" {
		b.WriteString(" asset=")
		b.WriteString(target.Asset)
	}
	return b.String()
}
