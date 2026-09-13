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

package httpapi

import (
	"encoding/json"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
)

func TestPnlHaltReasonDTOsSerializeRequiredAndOptionalFields(t *testing.T) {
	t.Run("account required", func(t *testing.T) {
		got := marshalDTOMap(t, toAccountDTO(domain.Account{
			PnlHaltReason: domain.PnlHaltReasonMissingInitialPnl,
		}, domain.AccountBlockState{}))
		if got["pnlHaltReason"] != "missing_initial_pnl" {
			t.Fatalf("pnlHaltReason = %v", got["pnlHaltReason"])
		}

		empty := marshalDTOMap(t, toAccountDTO(
			domain.Account{}, domain.AccountBlockState{},
		))
		if value, ok := empty["pnlHaltReason"]; !ok || value != "" {
			t.Fatalf("empty pnlHaltReason = %v, present = %v", value, ok)
		}
	})

	t.Run("balance required", func(t *testing.T) {
		got := marshalDTOMap(t, toBalanceDTO(domain.Balance{
			RealizedPnlHaltReason: domain.PnlHaltReasonMissingCostBasis,
		}))
		if got["realizedPnlHaltReason"] != "missing_cost_basis" {
			t.Fatalf("realizedPnlHaltReason = %v", got["realizedPnlHaltReason"])
		}

		empty := marshalDTOMap(t, toBalanceDTO(domain.Balance{}))
		if value, ok := empty["realizedPnlHaltReason"]; !ok || value != "" {
			t.Fatalf("empty realizedPnlHaltReason = %v, present = %v", value, ok)
		}
	})

	t.Run("adjustment outcome optional", func(t *testing.T) {
		got := marshalDTOMap(t, toAdjustmentDTO(domain.AccountAdjustmentRecord{
			Accepted: &domain.AdjustmentOutcomeAccepted{
				RealizedPnlHaltReason: domain.PnlHaltReasonArithmeticOverflow,
			},
		}))
		accepted := nestedDTOMap(t, nestedDTOMap(t, got, "outcome"), "accepted")
		if accepted["realizedPnlHaltReason"] != "arithmetic_overflow" {
			t.Fatalf("realizedPnlHaltReason = %v", accepted["realizedPnlHaltReason"])
		}

		empty := marshalDTOMap(t, toAdjustmentDTO(domain.AccountAdjustmentRecord{
			Accepted: &domain.AdjustmentOutcomeAccepted{},
		}))
		emptyAccepted := nestedDTOMap(t, nestedDTOMap(t, empty, "outcome"), "accepted")
		if _, ok := emptyAccepted["realizedPnlHaltReason"]; ok {
			t.Fatal("empty adjustment halt reason was serialized")
		}
	})

	t.Run("execution outcome optional", func(t *testing.T) {
		got := marshalDTOMap(t, toExecutionResultDTO(engine.ExecutionReportResult{
			Outcomes: []engine.BalanceOutcome{{
				Asset: "USD",
				Outcome: domain.AdjustmentOutcomeAccepted{
					RealizedPnlHaltReason: domain.PnlHaltReasonMissingFx,
				},
			}},
		}))
		outcomes, ok := got["outcomes"].([]any)
		if !ok || len(outcomes) != 1 {
			t.Fatalf("outcomes = %#v", got["outcomes"])
		}
		outcome := outcomes[0].(map[string]any)
		if outcome["realizedPnlHaltReason"] != "missing_fx" {
			t.Fatalf("realizedPnlHaltReason = %v", outcome["realizedPnlHaltReason"])
		}

		empty := marshalDTOMap(t, toExecutionResultDTO(engine.ExecutionReportResult{
			Outcomes: []engine.BalanceOutcome{{Asset: "USD"}},
		}))
		emptyOutcome := empty["outcomes"].([]any)[0].(map[string]any)
		if _, ok := emptyOutcome["realizedPnlHaltReason"]; ok {
			t.Fatal("empty execution halt reason was serialized")
		}
	})
}

func TestExecutionBlockDTOsPreserveSDKPolicy(t *testing.T) {
	t.Parallel()
	const policy = domain.PolicySpotFundsPnlBoundsKillSwitch
	execution := marshalDTOMap(t, toExecutionResultDTO(engine.ExecutionReportResult{
		Blocks: []domain.AccountBlock{{
			Account: "acc-1", Policy: policy,
			Code: "pnl_bound_breached", Reason: "lower bound breached",
		}},
	}))
	blocks, ok := execution["blocks"].([]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("execution blocks = %#v", execution["blocks"])
	}
	if got := blocks[0].(map[string]any)["policy"]; got != policy {
		t.Fatalf("execution block policy = %v, want %s", got, policy)
	}

	check := marshalDTOMap(t, toCheckResultDTO(domain.CheckResult{
		WouldBlock: &domain.AccountBlock{
			Account: "acc-1", Policy: policy, Code: "pnl_bound_breached",
		},
	}))
	if got := nestedDTOMap(t, check, "wouldBlock")["policy"]; got != policy {
		t.Fatalf("check block policy = %v, want %s", got, policy)
	}

	history := marshalDTOMap(t, executionResultFromPayload(domain.ApprovalPayload{
		Result: &domain.AttestationResult{Blocks: []domain.AttestationBlock{{
			Account: "acc-1", Policy: policy, Code: "pnl_bound_breached",
		}}},
	}))
	historyBlocks := history["blocks"].([]any)
	if got := historyBlocks[0].(map[string]any)["policy"]; got != policy {
		t.Fatalf("history block policy = %v, want %s", got, policy)
	}
}

func marshalDTOMap(t *testing.T, value any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func nestedDTOMap(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	child, ok := parent[key].(map[string]any)
	if !ok {
		t.Fatalf("%s = %#v", key, parent[key])
	}
	return child
}
