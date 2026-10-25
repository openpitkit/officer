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

package integration_test

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
)

// adjustmentsSource records the adjustment calls apply_adjustment makes and
// answers them with stored and the recorder's err.
type adjustmentsSource struct {
	fakeSource
	sourceRecorder
	stored domain.AccountAdjustmentRecord
}

func (s *adjustmentsSource) ApplyAdjustment(
	_ context.Context,
	account domain.AccountID,
	externalID domain.ExternalID,
	req domain.AdjustmentRequest,
	missing domain.MissingAccountPolicy,
) (domain.AccountAdjustmentRecord, error) {
	return s.stored, s.record("apply", account, externalID, req, missing)
}

// adjustmentsFixture answers with an accepted adjustment that differs from
// every input the tests send, so a result shows the record the core returned.
func adjustmentsFixture() *adjustmentsSource {
	return &adjustmentsSource{stored: domain.AccountAdjustmentRecord{
		At:         time.Date(2026, 10, 15, 9, 30, 0, 0, time.UTC),
		ExternalID: "adj-stored",
		Account:    "desk-2",
		Asset:      "EUR",
		Source:     domain.SourceMCP,
		Principal:  "operator",
		Request: domain.AdjustmentRequest{
			Type:    domain.AdjustmentTypeCorrection,
			Asset:   "EUR",
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: "7"},
		},
		Accepted: &domain.AdjustmentOutcomeAccepted{
			BalanceDelta: "2", BalanceResult: "7", HeldDelta: "0", HeldResult: "0",
			IncomingDelta: "0", IncomingResult: "0",
		},
	}}
}

// adjustmentsRecorded is adjustmentsFixture as the shared tool drivers take it.
func adjustmentsRecorded() (frameworkmcp.Source, *fakeSource, *sourceRecorder) {
	src := adjustmentsFixture()
	return src, &src.fakeSource, &src.sourceRecorder
}

// rejectedAdjustmentRecorded answers with the stored adjustment as the engine
// rejects one whose result leaves its bounds.
func rejectedAdjustmentRecorded() (frameworkmcp.Source, *fakeSource, *sourceRecorder) {
	src := adjustmentsFixture()
	src.stored.Accepted = nil
	src.stored.Rejected = &domain.AdjustmentOutcomeRejected{
		Code:    domain.RejectCodeAccountAdjustmentBoundsExceeded,
		Scope:   "account",
		Policy:  "SpotFunds",
		Reason:  "account adjustment bounds exceeded",
		Details: "asset EUR: balance 7, lower None, upper Some(5)",
	}
	return src, &src.fakeSource, &src.sourceRecorder
}

// noChangeRecorded answers every adjustment as one the engine reports as
// changing nothing.
func noChangeRecorded() (frameworkmcp.Source, *fakeSource, *sourceRecorder) {
	src := adjustmentsFixture()
	src.err = fmt.Errorf("apply adjustment: %w", domain.ErrNoChange)
	return src, &src.fakeSource, &src.sourceRecorder
}

// The REST adjustment bodies of the stored record, accepted and rejected.
const (
	storedAdjustmentHead = `{"adjustment":{"at":"2026-10-15T09:30:00Z","id":"adj-stored",` +
		`"request":{"balance":{"mode":"absolute","value":"7"},"type":"correction","asset":"EUR"},`
	storedAdjustmentTail   = `"account":"desk-2","principal":"operator","source":"mcp",`
	acceptedAdjustmentBody = storedAdjustmentHead +
		`"outcome":{"accepted":{"balanceDelta":"2","balanceResult":"7","heldDelta":"0",` +
		`"heldResult":"0","incomingDelta":"0","incomingResult":"0",` +
		`"realizedPnlResult":{"delta":"","result":""}}},` +
		storedAdjustmentTail + `"status":"accepted"}}`
	rejectedAdjustmentBody = storedAdjustmentHead +
		`"outcome":{"rejected":{"code":"account_adjustment_bounds_exceeded","scope":"account",` +
		`"policy":"SpotFunds","reason":"account adjustment bounds exceeded",` +
		`"details":"asset EUR: balance 7, lower None, upper Some(5)"}},` +
		storedAdjustmentTail + `"status":"rejected"}}`
)

// minimalAdjustmentInput names an account and its policy and the two required
// members of the body.
var minimalAdjustmentInput = map[string]any{
	"account": "desk-1", "missingAccount": "reject",
	"adjustment": map[string]any{"type": "deposit", "asset": "USD"},
}

// minimalAdjustmentArgs is what minimalAdjustmentInput reaches the Source as:
// no id, which leaves it to the store, and no amount or bound.
var minimalAdjustmentArgs = []any{
	domain.AccountID("desk-1"), domain.ExternalID(""),
	domain.AdjustmentRequest{Type: domain.AdjustmentTypeDeposit, Asset: "USD"},
	domain.MissingAccountReject,
}

// adjustmentInputs holds a valid apply_adjustment input.
var adjustmentInputs = map[string]map[string]any{
	"apply_adjustment": minimalAdjustmentInput,
}

// TestApplyAdjustmentSuccess covers apply_adjustment's success: the whole body
// reaches the Source verbatim, every member with a value no other one carries,
// spaces and decimal digits a parse would drop included, the id parsed and the
// policy as sent; the result is the REST body of the record, accepted or
// rejected, and a change the engine reports as none is a success without an
// adjustment, where REST answers 204.
func TestApplyAdjustmentSuccess(t *testing.T) {
	t.Parallel()
	full := map[string]any{
		"account": " desk-1 ", "missingAccount": " create ",
		"adjustment": map[string]any{
			"id": " adj-1 ", "type": " deposit ", "asset": " USD ",
			"averageEntryPrice": "101.250", "realizedPnl": "-3.50",
			"balance":        map[string]any{"mode": "delta", "value": "0.1000000000000000000001"},
			"balanceBounds":  map[string]any{"lower": "10", "upper": "11"},
			"held":           map[string]any{"mode": "absolute", "value": "2"},
			"heldBounds":     map[string]any{"lower": "20", "upper": "21"},
			"incoming":       map[string]any{"mode": " delta ", "value": "-4"},
			"incomingBounds": map[string]any{"lower": "30", "upper": "31"},
		},
	}
	fullArgs := []any{
		domain.AccountID(" desk-1 "), domain.ExternalID(" adj-1 "),
		domain.AdjustmentRequest{
			Type: " deposit ", Asset: " USD ", AverageEntryPrice: "101.250", RealizedPnl: "-3.50",
			Balance:        &domain.AdjustmentAmount{Mode: "delta", Value: "0.1000000000000000000001"},
			BalanceBounds:  &domain.AdjustmentBounds{Lower: "10", Upper: "11"},
			Held:           &domain.AdjustmentAmount{Mode: "absolute", Value: "2"},
			HeldBounds:     &domain.AdjustmentBounds{Lower: "20", Upper: "21"},
			Incoming:       &domain.AdjustmentAmount{Mode: " delta ", Value: "-4"},
			IncomingBounds: &domain.AdjustmentBounds{Lower: "30", Upper: "31"},
		},
		domain.MissingAccountPolicy(" create "),
	}
	runToolSuccess(t, adjustmentsRecorded, []toolSuccessCase{
		{"apply_adjustment", full, []string{"apply"}, fullArgs,
			"adjustment adj-stored accepted", acceptedAdjustmentBody},
		{"apply_adjustment", minimalAdjustmentInput, []string{"apply"}, minimalAdjustmentArgs,
			"adjustment adj-stored accepted", acceptedAdjustmentBody},
	})
	runToolSuccess(t, rejectedAdjustmentRecorded, []toolSuccessCase{
		{"apply_adjustment", minimalAdjustmentInput, []string{"apply"}, minimalAdjustmentArgs,
			"adjustment adj-stored rejected: account adjustment bounds exceeded",
			rejectedAdjustmentBody},
	})
	runToolSuccess(t, noChangeRecorded, []toolSuccessCase{
		{"apply_adjustment", minimalAdjustmentInput, []string{"apply"}, minimalAdjustmentArgs,
			"the engine reported no change; no adjustment was recorded", `{}`},
	})
}

// TestApplyAdjustmentSourceRefusal covers a Source refusal of apply_adjustment
// the core produces for this call: it reaches the agent under the code REST
// answers with the Source's message.
func TestApplyAdjustmentSourceRefusal(t *testing.T) {
	t.Parallel()
	runToolRefusals(t, adjustmentsRecorded, adjustmentInputs, []toolRefusal{
		{"validation", fmt.Errorf("engine: adjustment balance value %q: %w", "x", domain.ErrInvalid),
			`validation: engine: adjustment balance value "x": invalid`},
		{"account_missing", domain.NewAccountMissingError("desk-1"),
			`account_missing: account "desk-1" does not exist`},
		{"conflict", fmt.Errorf("adjustment %q: %w", "adj-1", domain.ErrAlreadyExists),
			`conflict: adjustment "adj-1": already exists`},
	})
}

// TestApplyAdjustmentDisabled covers apply_adjustment with its command
// disabled: the agent receives the panel notice and the Source is not called.
func TestApplyAdjustmentDisabled(t *testing.T) {
	t.Parallel()
	runToolsDisabled(t, adjustmentsRecorded, adjustmentInputs)
}

// TestApplyAdjustmentInputDescriptions covers the apply_adjustment input
// schema an agent reads: every member, those of the nested REST body
// included, carries a description.
func TestApplyAdjustmentInputDescriptions(t *testing.T) {
	t.Parallel()
	tool, ok := listToolsFor(t, &fakeSource{})["apply_adjustment"]
	if !ok {
		t.Fatal("apply_adjustment not advertised")
	}
	var undescribed []string
	var walk func(path string, schema *jsonschema.Schema)
	walk = func(path string, schema *jsonschema.Schema) {
		for _, name := range slices.Sorted(maps.Keys(schema.Properties)) {
			property := schema.Properties[name]
			if property.Description == "" {
				undescribed = append(undescribed, path+name)
			}
			walk(path+name+".", property)
		}
	}
	walk("", decodeSchema(t, tool.InputSchema))
	if len(undescribed) != 0 {
		t.Fatalf("input members without a description: %v", undescribed)
	}
}
