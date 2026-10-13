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

package httpapi

import "go.openpit.dev/officer/framework/engine"

// ExecutionResultDTO is the wire shape of one execution-report outcome: the
// account blocks the engine recorded and the per-asset adjustment outcomes.
type ExecutionResultDTO struct {
	Blocks   []ExecutionBlockDTO   `json:"blocks"`
	Outcomes []ExecutionOutcomeDTO `json:"outcomes"`
}

// ExecutionOutcomeDTO is one per-asset balance effect of a fill, tagged with its
// asset so the base and quote legs of a spot fill can be told apart.
type ExecutionOutcomeDTO struct {
	Asset                 string `json:"asset"`
	BalanceDelta          string `json:"balanceDelta"`
	BalanceResult         string `json:"balanceResult"`
	HeldDelta             string `json:"heldDelta"`
	HeldResult            string `json:"heldResult"`
	IncomingDelta         string `json:"incomingDelta"`
	IncomingResult        string `json:"incomingResult"`
	RealizedPnlDelta      string `json:"realizedPnlDelta"`
	RealizedPnlResult     string `json:"realizedPnlResult"`
	RealizedPnlHaltReason string `json:"realizedPnlHaltReason,omitempty"`
	AverageEntryPrice     string `json:"averageEntryPrice,omitempty"`
}

// ExecutionBlockDTO is one engine-recorded account block from a report.
type ExecutionBlockDTO struct {
	Account string `json:"account"`
	Policy  string `json:"policy"`
	Code    string `json:"code"`
	Reason  string `json:"reason"`
	Details string `json:"details"`
}

// ExecutionReportResponseDTO is the response of POST
// /orders/{id}/execution-reports: the recorded result plus the
// attestation envelope proving what Officer recorded. SignedAttestation is present
// on success; signing or attestation persistence failures fail the request
// before the report is committed.
type ExecutionReportResponseDTO struct {
	ID                string             `json:"id"`
	Result            ExecutionResultDTO `json:"result"`
	SignedAttestation string             `json:"signedAttestation,omitempty"`
	AttestationKeyID  string             `json:"attestationKeyId,omitempty"`
	Signed            bool               `json:"signed"`
}

// ToExecutionReportResponseDTO builds the response of POST
// /orders/{id}/execution-reports from the recorded engine result and the token,
// key id and signed flag of the report's attestation, empty and false when the
// report carries none.
func ToExecutionReportResponseDTO(
	result engine.ExecutionReportResult, token, keyID string, signed bool,
) ExecutionReportResponseDTO {
	return ExecutionReportResponseDTO{
		ID:                result.ReportID.String(),
		Result:            ToExecutionResultDTO(result),
		SignedAttestation: token,
		AttestationKeyID:  keyID,
		Signed:            signed,
	}
}

// ToExecutionResultDTO maps an engine.ExecutionReportResult onto the wire DTO.
func ToExecutionResultDTO(r engine.ExecutionReportResult) ExecutionResultDTO {
	blocks := make([]ExecutionBlockDTO, 0, len(r.Blocks))
	for _, b := range r.Blocks {
		blocks = append(blocks, ExecutionBlockDTO{
			Account: string(b.Account),
			Policy:  b.Policy,
			Code:    b.Code,
			Reason:  b.Reason,
			Details: b.Details,
		})
	}
	outcomes := make([]ExecutionOutcomeDTO, 0, len(r.Outcomes))
	for _, o := range r.Outcomes {
		outcomes = append(outcomes, ExecutionOutcomeDTO{
			Asset:                 o.Asset,
			BalanceDelta:          o.Outcome.BalanceDelta,
			BalanceResult:         o.Outcome.BalanceResult,
			HeldDelta:             o.Outcome.HeldDelta,
			HeldResult:            o.Outcome.HeldResult,
			IncomingDelta:         o.Outcome.IncomingDelta,
			IncomingResult:        o.Outcome.IncomingResult,
			RealizedPnlDelta:      o.Outcome.RealizedPnlDelta,
			RealizedPnlResult:     o.Outcome.RealizedPnlResult,
			RealizedPnlHaltReason: string(o.Outcome.RealizedPnlHaltReason),
			AverageEntryPrice:     o.Outcome.AverageEntryPrice,
		})
	}
	return ExecutionResultDTO{Blocks: blocks, Outcomes: outcomes}
}
