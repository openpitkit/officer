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

import (
	"time"

	"go.openpit.dev/officer/framework/domain"
)

// The DTOs below are the JSON wire shapes of an account adjustment request and
// of the recorded adjustment. The REST surface and the MCP tools share them so
// both accept and return the same body.

// AdjustmentAmountDTO is one per-field adjustment value in a request body.
type AdjustmentAmountDTO struct {
	Mode  string `json:"mode" jsonschema:"absolute sets the field to value, delta adds value to it"`
	Value string `json:"value" jsonschema:"Exact decimal string; a negative delta decreases the field"`
}

// AdjustmentBoundsDTO constrains an adjustment field's resulting value.
type AdjustmentBoundsDTO struct {
	Lower string `json:"lower,omitempty" jsonschema:"Inclusive lower bound on the resulting value as an exact decimal string; omitted sets none"`
	Upper string `json:"upper,omitempty" jsonschema:"Inclusive upper bound on the resulting value as an exact decimal string; omitted sets none"`
}

// AdjustmentRequestDTO is the wire body of POST .../adjustments. All values are
// exact decimal strings passed through verbatim. Type is the required
// adjustment type, also passed through verbatim: the node refuses a missing or
// unknown one, and the echoed request always carries it. ID is the optional
// caller-supplied opaque public handle for the adjustment record; when omitted
// the server generates and returns one. It is
// write-only on create: a supplied id is accepted verbatim and must be unique
// (a duplicate is a 409). It is omitted from the request echoed back in an
// adjustment record (the record's own id carries the resolved handle).
type AdjustmentRequestDTO struct {
	Balance           *AdjustmentAmountDTO `json:"balance,omitempty" jsonschema:"Change of the available balance; omitted leaves it unchanged"`
	BalanceBounds     *AdjustmentBoundsDTO `json:"balanceBounds,omitempty" jsonschema:"Bounds on the available balance the adjustment results in, not on the change, checked only when balance is given; a result outside them records the adjustment as rejected with its reason and changes no balance"`
	Held              *AdjustmentAmountDTO `json:"held,omitempty" jsonschema:"Change of the held balance; omitted leaves it unchanged"`
	HeldBounds        *AdjustmentBoundsDTO `json:"heldBounds,omitempty" jsonschema:"Bounds on the held balance the adjustment results in, not on the change, checked only when held is given; a result outside them records the adjustment as rejected with its reason and changes no balance"`
	Incoming          *AdjustmentAmountDTO `json:"incoming,omitempty" jsonschema:"Change of the incoming balance; omitted leaves it unchanged"`
	IncomingBounds    *AdjustmentBoundsDTO `json:"incomingBounds,omitempty" jsonschema:"Bounds on the incoming balance the adjustment results in, not on the change, checked only when incoming is given; a result outside them records the adjustment as rejected with its reason and changes no balance"`
	ID                string               `json:"id,omitempty" jsonschema:"Optional caller-supplied unique adjustment id; omit to have the server generate one. A duplicate is refused with conflict. An id starting with trading: is reserved for the trading runtime and refused for an agent"`
	Type              string               `json:"type" jsonschema:"Reason recorded with the adjustment: venue_fee (a fee or charge a trading venue booked), deposit (funds deposited into the account), withdrawal (funds withdrawn from the account) or correction (a manual correction of the books). It does not change how the amounts apply, so a withdrawal takes a negative delta"`
	Asset             string               `json:"asset" jsonschema:"Code of the asset whose balances are adjusted. For an agent it must be the account's own currency, an asset that exists; from an operator an asset that does not exist yet is created"`
	AverageEntryPrice string               `json:"averageEntryPrice,omitempty" jsonschema:"Optional replacement for the average entry price of the asset's position as an exact decimal string; for an agent refused unless equal to the current value"`
	RealizedPnl       string               `json:"realizedPnl,omitempty" jsonschema:"Optional replacement for the asset's cumulative realized P&L as an exact decimal string; from an operator it restarts a halted P&L calculation from this value, and for an agent it is refused unless equal to the current value, and always while the calculation is halted"`
}

// AdjustmentOutcomeDTO is the accept/reject outcome of an adjustment record.
// Exactly one of Accepted/Rejected is non-nil.
type AdjustmentOutcomeDTO struct {
	Accepted *AdjustmentAcceptedDTO `json:"accepted,omitempty"`
	Rejected *AdjustmentRejectedDTO `json:"rejected,omitempty"`
}

// AdjustmentAcceptedDTO carries the per-field delta and absolute result.
type AdjustmentAcceptedDTO struct {
	BalanceDelta          string                         `json:"balanceDelta"`
	BalanceResult         string                         `json:"balanceResult"`
	HeldDelta             string                         `json:"heldDelta"`
	HeldResult            string                         `json:"heldResult"`
	IncomingDelta         string                         `json:"incomingDelta"`
	IncomingResult        string                         `json:"incomingResult"`
	RealizedPnlResult     AdjustmentRealizedPnlResultDTO `json:"realizedPnlResult"`
	RealizedPnlHaltReason string                         `json:"realizedPnlHaltReason,omitempty"`
	AverageEntryPrice     string                         `json:"averageEntryPrice,omitempty"`
}

// AdjustmentRealizedPnlResultDTO is the realized-P&L delta and absolute result
// of an accepted adjustment.
type AdjustmentRealizedPnlResultDTO struct {
	Delta  string `json:"delta"`
	Result string `json:"result"`
}

// AdjustmentRejectedDTO carries the structured rejection reason.
type AdjustmentRejectedDTO struct {
	Code    string `json:"code"`
	Scope   string `json:"scope,omitempty"`
	Policy  string `json:"policy,omitempty"`
	Reason  string `json:"reason"`
	Details string `json:"details,omitempty"`
}

// AdjustmentDTO is the wire shape of one adjustment record incl. outcome. An
// adjustment is a machine record: its public handle is the opaque external id;
// no surrogate id is serialized.
type AdjustmentDTO struct {
	At        time.Time            `json:"at"`
	ID        string               `json:"id"`
	Request   AdjustmentRequestDTO `json:"request"`
	Outcome   AdjustmentOutcomeDTO `json:"outcome"`
	Account   string               `json:"account"`
	Principal string               `json:"principal,omitempty"`
	Source    string               `json:"source"`
	Status    string               `json:"status"`
}

// FromAdjustmentRequestDTO maps a wire request body onto the domain request.
// Decimal values are carried through verbatim; never parsed to float.
func FromAdjustmentRequestDTO(dto AdjustmentRequestDTO) domain.AdjustmentRequest {
	return domain.AdjustmentRequest{
		Type:              domain.AdjustmentType(dto.Type),
		Asset:             dto.Asset,
		AverageEntryPrice: dto.AverageEntryPrice,
		RealizedPnl:       dto.RealizedPnl,
		Balance:           fromAdjustmentAmountDTO(dto.Balance),
		BalanceBounds:     fromAdjustmentBoundsDTO(dto.BalanceBounds),
		Held:              fromAdjustmentAmountDTO(dto.Held),
		HeldBounds:        fromAdjustmentBoundsDTO(dto.HeldBounds),
		Incoming:          fromAdjustmentAmountDTO(dto.Incoming),
		IncomingBounds:    fromAdjustmentBoundsDTO(dto.IncomingBounds),
	}
}

func fromAdjustmentAmountDTO(dto *AdjustmentAmountDTO) *domain.AdjustmentAmount {
	if dto == nil {
		return nil
	}
	return &domain.AdjustmentAmount{
		Mode:  domain.AdjustmentAmountMode(dto.Mode),
		Value: dto.Value,
	}
}

func fromAdjustmentBoundsDTO(dto *AdjustmentBoundsDTO) *domain.AdjustmentBounds {
	if dto == nil {
		return nil
	}
	return &domain.AdjustmentBounds{Lower: dto.Lower, Upper: dto.Upper}
}

func toAdjustmentRequestDTO(req domain.AdjustmentRequest) AdjustmentRequestDTO {
	return AdjustmentRequestDTO{
		Type:              string(req.Type),
		Asset:             req.Asset,
		AverageEntryPrice: req.AverageEntryPrice,
		RealizedPnl:       req.RealizedPnl,
		Balance:           toAdjustmentAmountDTO(req.Balance),
		BalanceBounds:     toAdjustmentBoundsDTO(req.BalanceBounds),
		Held:              toAdjustmentAmountDTO(req.Held),
		HeldBounds:        toAdjustmentBoundsDTO(req.HeldBounds),
		Incoming:          toAdjustmentAmountDTO(req.Incoming),
		IncomingBounds:    toAdjustmentBoundsDTO(req.IncomingBounds),
	}
}

func toAdjustmentAmountDTO(a *domain.AdjustmentAmount) *AdjustmentAmountDTO {
	if a == nil {
		return nil
	}
	return &AdjustmentAmountDTO{Mode: string(a.Mode), Value: a.Value}
}

func toAdjustmentBoundsDTO(b *domain.AdjustmentBounds) *AdjustmentBoundsDTO {
	if b == nil {
		return nil
	}
	return &AdjustmentBoundsDTO{Lower: b.Lower, Upper: b.Upper}
}

// ToAdjustmentDTO maps a domain.AccountAdjustmentRecord onto the wire DTO. The
// status field summarises the outcome for indexed listing; the outcome object
// carries the full accepted-or-rejected detail.
func ToAdjustmentDTO(r domain.AccountAdjustmentRecord) AdjustmentDTO {
	status := domain.AdjustmentStatusAccepted
	outcome := AdjustmentOutcomeDTO{}
	if r.Accepted != nil {
		outcome.Accepted = &AdjustmentAcceptedDTO{
			BalanceDelta:          r.Accepted.BalanceDelta,
			BalanceResult:         r.Accepted.BalanceResult,
			HeldDelta:             r.Accepted.HeldDelta,
			HeldResult:            r.Accepted.HeldResult,
			IncomingDelta:         r.Accepted.IncomingDelta,
			IncomingResult:        r.Accepted.IncomingResult,
			RealizedPnlResult:     toAdjustmentRealizedPnlResultDTO(r),
			RealizedPnlHaltReason: string(r.Accepted.RealizedPnlHaltReason),
			AverageEntryPrice:     r.Accepted.AverageEntryPrice,
		}
	}
	if r.Rejected != nil {
		status = domain.AdjustmentStatusRejected
		outcome.Rejected = &AdjustmentRejectedDTO{
			Code:    r.Rejected.Code,
			Scope:   r.Rejected.Scope,
			Policy:  r.Rejected.Policy,
			Reason:  r.Rejected.Reason,
			Details: r.Rejected.Details,
		}
	}
	return AdjustmentDTO{
		At:        r.At,
		ID:        r.ExternalID.String(),
		Request:   toAdjustmentRequestDTO(r.Request),
		Outcome:   outcome,
		Account:   string(r.Account),
		Principal: r.Principal,
		Source:    string(r.Source),
		Status:    string(status),
	}
}

// toAdjustmentRealizedPnlResultDTO surfaces the persisted realized-P&L result and
// delta verbatim from the accepted outcome, which the node guarantees carries the
// authoritative persisted value; no fallback to the request is applied, so delta
// and result stay mutually consistent.
func toAdjustmentRealizedPnlResultDTO(
	r domain.AccountAdjustmentRecord,
) AdjustmentRealizedPnlResultDTO {
	return AdjustmentRealizedPnlResultDTO{
		Delta:  r.Accepted.RealizedPnlDelta,
		Result: r.Accepted.RealizedPnlResult,
	}
}
