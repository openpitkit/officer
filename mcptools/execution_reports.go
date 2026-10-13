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

package mcptools

import (
	"context"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

// report_fill maps its JSON arguments 1:1 onto the execution report REST
// builds, without REST's force, and leaves every value rule - the status, the
// fill, leaves, the lock price and the commission - to the core, which also
// decides whether the report is signed.

const reportFillToolName = "report_fill"

const reportFillToolDescription = "Apply an execution report - a fill, a " +
	"commission or a lifecycle status - to an order returned by submit_order " +
	"or submit_drop_copy_order; the instrument, account and side come from the " +
	"order. A report on an ordinary order is signed and attested fail-closed " +
	"and returns its attestation, unsigned under eSign-off; a report on a " +
	"drop-copy order stays unsigned and returns no attestation, because " +
	"drop-copy never enforced a pre-trade verdict. filled and partially_filled " +
	"carry a fill; rejected, rolled_back, filled and cancelled are terminal and " +
	"release the order's remaining reservation. A report on a terminal order is " +
	"refused with terminal_order, on a venue-linked order with " +
	"venue_order_managed, and a repeated id with conflict. Mutates engine " +
	"state; protected and disabled by default."

type reportFillInput struct {
	Order          string               `json:"order" jsonschema:"Order id returned by submit_order or submit_drop_copy_order"`
	ExternalID     string               `json:"id,omitempty" jsonschema:"Optional caller-supplied unique report id; omit to have the store assign one; a repeated id is refused with conflict"`
	Status         string               `json:"status" jsonschema:"Target lifecycle status: submitted, accepted, rejected, committed, rolled_back, filled, partially_filled or cancelled; rejected, rolled_back, filled and cancelled are terminal"`
	Quantity       string               `json:"quantity,omitempty" jsonschema:"Fill quantity as an exact decimal string; required with price for filled and partially_filled and refused for every other status"`
	Price          string               `json:"price,omitempty" jsonschema:"Fill price as an exact decimal string; required with quantity for filled and partially_filled and refused for every other status"`
	LeavesQuantity string               `json:"leavesQuantity,omitempty" jsonschema:"Caller-reported open base quantity as a non-negative plain decimal string, recorded verbatim on the order, the report event and the attestation; required with a fill, optional otherwise; the SDK receives the order's own recorded reservation remainder"`
	LockPrice      string               `json:"lockPrice,omitempty" jsonschema:"Display lock price as a plain decimal string without surrounding space or exponent, recorded on the report event, the trade and the attestation; settlement uses the order's stored pre-trade lock, never this value; refused for the non-terminal workflow statuses submitted, accepted and committed"`
	Commission     *httpx.CommissionDTO `json:"commission,omitempty" jsonschema:"Optional fee (positive amount) or rebate (negative amount) with its currency code, forwarded to the engine as one value without currency conversion; omit for none, and when present amount and currency must both be non-empty"`
}

// reportFillHandler applies the report and answers with the REST response
// body: the recorded result with the attestation the core returned, none for
// a drop-copy order.
func reportFillHandler(
	src frameworkmcp.Source,
) frameworkmcp.ToolBody[reportFillInput, httpx.ExecutionReportResponseDTO] {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		in reportFillInput,
	) (string, httpx.ExecutionReportResponseDTO, error) {
		report := domain.ExecutionReportInput{
			FillQuantity:   in.Quantity,
			FillPrice:      in.Price,
			LeavesQuantity: in.LeavesQuantity,
			LockPrice:      in.LockPrice,
			OrderStatus:    domain.OrderStatus(in.Status),
		}
		// A present commission reaches the core as given: unlike the REST
		// form, an all-empty one is not read as none, so the core refuses it.
		if in.Commission != nil {
			report.Commission = &domain.Commission{
				Amount:   in.Commission.Amount,
				Currency: in.Commission.Currency,
			}
		}
		if in.ExternalID != "" {
			id, err := domain.ParseExternalID(in.ExternalID)
			if err != nil {
				return "", httpx.ExecutionReportResponseDTO{}, toolError(err)
			}
			report.ExternalID = id
		}
		order, err := domain.ParseExternalID(in.Order)
		if err != nil {
			return "", httpx.ExecutionReportResponseDTO{}, toolError(err)
		}
		report.Order = order
		result, att, err := src.ApplyExecutionReport(ctx, report)
		if err != nil {
			return "", httpx.ExecutionReportResponseDTO{}, toolError(err)
		}
		out := httpx.ToExecutionReportResponseDTO(
			result, att.Token, att.KeyID, att.Signed,
		)
		return fmt.Sprintf(
			"execution report %s recorded for order %s: %s",
			out.ID, order, report.OrderStatus,
		), out, nil
	}
}
