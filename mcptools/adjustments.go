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
	"errors"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

// The adjustment tool maps its JSON arguments 1:1 onto the adjustment call REST
// makes: the body is REST's, its decimal strings reach the core verbatim, and
// the core applies the amounts and the bounds and validates every value, the
// account and the missing-account policy.

const applyAdjustmentToolName = "apply_adjustment"

const applyAdjustmentToolDescription = "Apply an adjustment to an account's " +
	"balances of one asset through the engine and return the recorded " +
	"adjustment. An agent may only lower the balance (available), held and " +
	"incoming of the account's own currency, each set to a value (absolute) or " +
	"changed by one (delta), given as exact decimal strings: another asset, a " +
	"position included, an account without a currency of its own, a delta " +
	"above zero or an absolute above the field's current value refuses the " +
	"whole adjustment with loosening_refused and changes nothing, and so does " +
	"an averageEntryPrice or a realizedPnl other than the current one, or any " +
	"realizedPnl while the position's P&L calculation is halted, as it would " +
	"restart it. A bound constrains the value the adjustment results in, not the change: a " +
	"result outside it records the adjustment as rejected with a structured " +
	"reason and changes no balance, a policy outcome rather than an error. An " +
	"adjustment that changes nothing, such as one naming no field, is not " +
	"recorded and returns no adjustment. Mutates account balances; protected " +
	"and disabled by default."

type applyAdjustmentInput struct {
	Account        string                     `json:"account" jsonschema:"Account code"`
	MissingAccount string                     `json:"missingAccount" jsonschema:"Required: create or reject; for an account that does not exist yet, create is refused for an agent with loosening_refused, and reject fails the adjustment instead"`
	Adjustment     httpx.AdjustmentRequestDTO `json:"adjustment" jsonschema:"Adjustment to apply to the account's balances of one asset"`
}

// applyAdjustmentOutput is the REST adjustment body, {"adjustment": {...}},
// and carries no adjustment when nothing changed, where REST answers 204.
type applyAdjustmentOutput struct {
	Adjustment *httpx.AdjustmentDTO `json:"adjustment,omitempty"`
}

// applyAdjustmentHandler applies the adjustment as REST does: a supplied id is
// parsed by the core, an omitted one is left to the store, and the policy of
// a named account goes to the backend, which validates it.
func applyAdjustmentHandler(
	src frameworkmcp.Source,
) frameworkmcp.ToolBody[applyAdjustmentInput, applyAdjustmentOutput] {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		in applyAdjustmentInput,
	) (string, applyAdjustmentOutput, error) {
		var externalID domain.ExternalID
		if in.Adjustment.ID != "" {
			id, err := domain.ParseExternalID(in.Adjustment.ID)
			if err != nil {
				return "", applyAdjustmentOutput{}, toolError(err)
			}
			externalID = id
		}
		account := domain.AccountID(in.Account)
		record, err := src.ApplyAdjustment(
			ctx,
			account,
			externalID,
			httpx.FromAdjustmentRequestDTO(in.Adjustment),
			missingAccountPolicy(account, in.MissingAccount),
		)
		if errors.Is(err, domain.ErrNoChange) {
			return "the engine reported no change; no adjustment was recorded", applyAdjustmentOutput{}, nil
		}
		if err != nil {
			return "", applyAdjustmentOutput{}, toolError(err)
		}
		adjustment := httpx.ToAdjustmentDTO(record)
		text := fmt.Sprintf("adjustment %s accepted", adjustment.ID)
		if rejected := adjustment.Outcome.Rejected; rejected != nil {
			text = fmt.Sprintf("adjustment %s rejected: %s", adjustment.ID, rejected.Reason)
		}
		return text, applyAdjustmentOutput{Adjustment: &adjustment}, nil
	}
}
