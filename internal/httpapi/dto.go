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
	"time"

	"go.openpit.dev/officer/internal/backend"
	"go.openpit.dev/officer/internal/domain"
	"go.openpit.dev/officer/internal/engine"
)

// The DTOs below are the JSON wire contract of the HTTP surface. They carry the
// camelCase tags the dashboard SPA expects. Domain and control-plane types
// carry no JSON tags; this package owns the encoding and the mapping.

// healthDTO is the body of GET /api/v1/health.
type healthDTO struct {
	OK bool `json:"ok"`
}

// statusDTO is the body of GET /api/v1/status.
type statusDTO struct {
	Nodes   []nodeHealthDTO `json:"nodes"`
	Healthy bool            `json:"healthy"`
}

// nodeHealthDTO is one node's health within statusDTO.
type nodeHealthDTO struct {
	Engine engineHealthDTO `json:"engine"`
	Store  storeHealthDTO  `json:"store"`
}

// engineHealthDTO is a node's engine health.
type engineHealthDTO struct {
	Version      string `json:"version"`
	BuildProfile string `json:"buildProfile"`
	Running      bool   `json:"running"`
}

// storeHealthDTO is a node's store health.
type storeHealthDTO struct {
	Path          string `json:"path"`
	SchemaVersion int    `json:"schemaVersion"`
	Reachable     bool   `json:"reachable"`
}

// accountDTO is the wire shape of a single account.
type accountDTO struct {
	ID          string `json:"id"`
	Group       string `json:"group"`
	Notes       string `json:"notes"`
	BlockReason string `json:"blockReason"`
	Blocked     bool   `json:"blocked"`
}

// limitDTO is the wire shape of a single risk barrier.
type limitDTO struct {
	Policy  string            `json:"policy"`
	Scope   string            `json:"scope"`
	Account string            `json:"account"`
	Asset   string            `json:"asset"`
	Values  map[string]string `json:"values"`
}

// auditDTO is the wire shape of a single audit row.
type auditDTO struct {
	At      time.Time `json:"at"`
	Actor   string    `json:"actor"`
	Action  string    `json:"action"`
	Account string    `json:"account"`
	Detail  string    `json:"detail"`
	Source  string    `json:"source"`
	ID      int64     `json:"id"`
}

// toStatusDTO maps a backend.Status onto the wire DTO.
func toStatusDTO(status backend.Status) statusDTO {
	nodes := make([]nodeHealthDTO, 0, len(status.Nodes))
	for _, n := range status.Nodes {
		nodes = append(nodes, nodeHealthDTO{
			Engine: engineHealthDTO{
				Version:      n.Engine.Version,
				BuildProfile: n.Engine.BuildProfile,
				Running:      n.Engine.Running,
			},
			Store: storeHealthDTO{
				Path:          n.Store.Path,
				SchemaVersion: n.Store.SchemaVersion,
				Reachable:     n.Store.Reachable,
			},
		})
	}
	return statusDTO{Nodes: nodes, Healthy: status.Healthy}
}

// toAccountDTO maps a domain.Account onto the wire DTO.
func toAccountDTO(a domain.Account) accountDTO {
	return accountDTO{
		ID:          string(a.ID),
		Group:       a.GroupID,
		Notes:       a.Notes,
		BlockReason: a.BlockReason,
		Blocked:     a.Blocked,
	}
}

// toLimitDTO maps a domain.Limit onto the wire DTO.
func toLimitDTO(l domain.Limit) limitDTO {
	vals := make(map[string]string, len(l.Values))
	for _, v := range l.Values {
		vals[v.Kind] = v.Value
	}
	return limitDTO{
		Policy:  l.Target.Policy,
		Scope:   l.Target.Scope,
		Account: string(l.Target.Account),
		Asset:   l.Target.Asset,
		Values:  vals,
	}
}

// fromLimitDTO maps a wire limitDTO back to a domain.Limit.
func fromLimitDTO(dto limitDTO) domain.Limit {
	vals := make([]domain.LimitValue, 0, len(dto.Values))
	for k, v := range dto.Values {
		vals = append(vals, domain.LimitValue{Kind: k, Value: v})
	}
	domain.SortLimitValues(vals)
	return domain.Limit{
		Target: domain.LimitTarget{
			Policy:  dto.Policy,
			Scope:   dto.Scope,
			Account: domain.AccountID(dto.Account),
			Asset:   dto.Asset,
		},
		Values: vals,
	}
}

// toAuditDTO maps a domain.AuditRow onto the wire DTO.
func toAuditDTO(row domain.AuditRow) auditDTO {
	return auditDTO{
		ID:      row.ID,
		At:      row.At,
		Actor:   row.Actor,
		Action:  string(row.Action),
		Account: string(row.Account),
		Detail:  row.Detail,
		Source:  string(row.Source),
	}
}

// --- group ------------------------------------------------------------------

// groupDTO is the wire shape of a single account group.
type groupDTO struct {
	ID          string `json:"id"`
	Notes       string `json:"notes"`
	BlockReason string `json:"blockReason"`
	Blocked     bool   `json:"blocked"`
}

// toGroupDTO maps a domain.AccountGroup onto the wire DTO.
func toGroupDTO(g domain.AccountGroup) groupDTO {
	return groupDTO{
		ID:          g.ID,
		Notes:       g.Notes,
		BlockReason: g.BlockReason,
		Blocked:     g.Blocked,
	}
}

// --- balance ----------------------------------------------------------------

// balanceDTO is the wire shape of one per-(account, asset) holdings snapshot.
// All amounts are exact decimal strings passed through verbatim.
type balanceDTO struct {
	UpdatedAt         time.Time `json:"updatedAt"`
	Account           string    `json:"account"`
	Asset             string    `json:"asset"`
	Available         string    `json:"available"`
	Held              string    `json:"held"`
	Incoming          string    `json:"incoming"`
	AverageEntryPrice string    `json:"averageEntryPrice"`
}

// toBalanceDTO maps a domain.Balance onto the wire DTO.
func toBalanceDTO(b domain.Balance) balanceDTO {
	return balanceDTO{
		UpdatedAt:         b.UpdatedAt,
		Account:           string(b.Account),
		Asset:             b.Asset,
		Available:         b.Available,
		Held:              b.Held,
		Incoming:          b.Incoming,
		AverageEntryPrice: b.AverageEntryPrice,
	}
}

// --- adjustment -------------------------------------------------------------

// adjustmentAmountDTO is one per-field adjustment value in a request body.
type adjustmentAmountDTO struct {
	Mode  string `json:"mode"`
	Value string `json:"value"`
}

// adjustmentBoundsDTO constrains an adjustment field's resulting value.
type adjustmentBoundsDTO struct {
	Lower string `json:"lower,omitempty"`
	Upper string `json:"upper,omitempty"`
}

// adjustmentRequestDTO is the wire body of POST .../adjustments. All values are
// exact decimal strings passed through verbatim.
type adjustmentRequestDTO struct {
	Balance           *adjustmentAmountDTO `json:"balance,omitempty"`
	BalanceBounds     *adjustmentBoundsDTO `json:"balanceBounds,omitempty"`
	Held              *adjustmentAmountDTO `json:"held,omitempty"`
	HeldBounds        *adjustmentBoundsDTO `json:"heldBounds,omitempty"`
	Incoming          *adjustmentAmountDTO `json:"incoming,omitempty"`
	IncomingBounds    *adjustmentBoundsDTO `json:"incomingBounds,omitempty"`
	Asset             string               `json:"asset"`
	AverageEntryPrice string               `json:"averageEntryPrice,omitempty"`
}

// adjustmentOutcomeDTO is the accept/reject outcome of an adjustment record.
// Exactly one of Accepted/Rejected is non-nil.
type adjustmentOutcomeDTO struct {
	Accepted *adjustmentAcceptedDTO `json:"accepted,omitempty"`
	Rejected *adjustmentRejectedDTO `json:"rejected,omitempty"`
}

// adjustmentAcceptedDTO carries the per-field delta and absolute result.
type adjustmentAcceptedDTO struct {
	BalanceDelta   string `json:"balanceDelta"`
	BalanceResult  string `json:"balanceResult"`
	HeldDelta      string `json:"heldDelta"`
	HeldResult     string `json:"heldResult"`
	IncomingDelta  string `json:"incomingDelta"`
	IncomingResult string `json:"incomingResult"`
}

// adjustmentRejectedDTO carries the structured rejection reason.
type adjustmentRejectedDTO struct {
	Code    string `json:"code"`
	Scope   string `json:"scope,omitempty"`
	Policy  string `json:"policy,omitempty"`
	Reason  string `json:"reason"`
	Details string `json:"details,omitempty"`
}

// adjustmentDTO is the wire shape of one adjustment record incl. outcome.
type adjustmentDTO struct {
	At      time.Time            `json:"at"`
	Request adjustmentRequestDTO `json:"request"`
	Outcome adjustmentOutcomeDTO `json:"outcome"`
	Account string               `json:"account"`
	Source  string               `json:"source"`
	Status  string               `json:"status"`
	ID      int64                `json:"id"`
}

// fromAdjustmentRequestDTO maps a wire request body onto the domain request.
// Decimal values are carried through verbatim; never parsed to float.
func fromAdjustmentRequestDTO(dto adjustmentRequestDTO) domain.AdjustmentRequest {
	return domain.AdjustmentRequest{
		Asset:             dto.Asset,
		AverageEntryPrice: dto.AverageEntryPrice,
		Balance:           fromAdjustmentAmountDTO(dto.Balance),
		BalanceBounds:     fromAdjustmentBoundsDTO(dto.BalanceBounds),
		Held:              fromAdjustmentAmountDTO(dto.Held),
		HeldBounds:        fromAdjustmentBoundsDTO(dto.HeldBounds),
		Incoming:          fromAdjustmentAmountDTO(dto.Incoming),
		IncomingBounds:    fromAdjustmentBoundsDTO(dto.IncomingBounds),
	}
}

func fromAdjustmentAmountDTO(dto *adjustmentAmountDTO) *domain.AdjustmentAmount {
	if dto == nil {
		return nil
	}
	return &domain.AdjustmentAmount{
		Mode:  domain.AdjustmentAmountMode(dto.Mode),
		Value: dto.Value,
	}
}

func fromAdjustmentBoundsDTO(dto *adjustmentBoundsDTO) *domain.AdjustmentBounds {
	if dto == nil {
		return nil
	}
	return &domain.AdjustmentBounds{Lower: dto.Lower, Upper: dto.Upper}
}

func toAdjustmentRequestDTO(req domain.AdjustmentRequest) adjustmentRequestDTO {
	return adjustmentRequestDTO{
		Asset:             req.Asset,
		AverageEntryPrice: req.AverageEntryPrice,
		Balance:           toAdjustmentAmountDTO(req.Balance),
		BalanceBounds:     toAdjustmentBoundsDTO(req.BalanceBounds),
		Held:              toAdjustmentAmountDTO(req.Held),
		HeldBounds:        toAdjustmentBoundsDTO(req.HeldBounds),
		Incoming:          toAdjustmentAmountDTO(req.Incoming),
		IncomingBounds:    toAdjustmentBoundsDTO(req.IncomingBounds),
	}
}

func toAdjustmentAmountDTO(a *domain.AdjustmentAmount) *adjustmentAmountDTO {
	if a == nil {
		return nil
	}
	return &adjustmentAmountDTO{Mode: string(a.Mode), Value: a.Value}
}

func toAdjustmentBoundsDTO(b *domain.AdjustmentBounds) *adjustmentBoundsDTO {
	if b == nil {
		return nil
	}
	return &adjustmentBoundsDTO{Lower: b.Lower, Upper: b.Upper}
}

// toAdjustmentDTO maps a domain.AccountAdjustmentRecord onto the wire DTO. The
// status field summarises the outcome for indexed listing; the outcome object
// carries the full accepted-or-rejected detail.
func toAdjustmentDTO(r domain.AccountAdjustmentRecord) adjustmentDTO {
	status := domain.AdjustmentStatusAccepted
	outcome := adjustmentOutcomeDTO{}
	if r.Accepted != nil {
		outcome.Accepted = &adjustmentAcceptedDTO{
			BalanceDelta:   r.Accepted.BalanceDelta,
			BalanceResult:  r.Accepted.BalanceResult,
			HeldDelta:      r.Accepted.HeldDelta,
			HeldResult:     r.Accepted.HeldResult,
			IncomingDelta:  r.Accepted.IncomingDelta,
			IncomingResult: r.Accepted.IncomingResult,
		}
	}
	if r.Rejected != nil {
		status = domain.AdjustmentStatusRejected
		outcome.Rejected = &adjustmentRejectedDTO{
			Code:    r.Rejected.Code,
			Scope:   r.Rejected.Scope,
			Policy:  r.Rejected.Policy,
			Reason:  r.Rejected.Reason,
			Details: r.Rejected.Details,
		}
	}
	return adjustmentDTO{
		At:      r.At,
		Request: toAdjustmentRequestDTO(r.Request),
		Outcome: outcome,
		Account: string(r.Account),
		Source:  string(r.Source),
		Status:  string(status),
		ID:      r.ID,
	}
}

// --- order ------------------------------------------------------------------

// orderDTO is the wire shape of one Officer-side order record. All monetary and
// size values are exact decimal strings passed through verbatim.
type orderDTO struct {
	At          time.Time `json:"at"`
	Account     string    `json:"account"`
	BaseAsset   string    `json:"baseAsset"`
	QuoteAsset  string    `json:"quoteAsset"`
	Side        string    `json:"side"`
	AmountKind  string    `json:"amountKind"`
	AmountValue string    `json:"amountValue"`
	Price       string    `json:"price"`
	Status      string    `json:"status"`
	Source      string    `json:"source"`
	LockPrices  []string  `json:"lockPrices"`
	ID          int64     `json:"id"`
}

// toOrderDTO maps a domain.Order onto the wire DTO.
func toOrderDTO(o domain.Order) orderDTO {
	prices := o.LockPrices
	if prices == nil {
		prices = []string{}
	}
	return orderDTO{
		At:          o.At,
		Account:     string(o.Account),
		BaseAsset:   o.BaseAsset,
		QuoteAsset:  o.QuoteAsset,
		Side:        string(o.Side),
		AmountKind:  string(o.AmountKind),
		AmountValue: o.AmountValue,
		Price:       o.Price,
		Status:      string(o.Status),
		Source:      string(o.Source),
		LockPrices:  prices,
		ID:          o.ID,
	}
}

// --- order event ------------------------------------------------------------

// orderEventDTO is the wire shape of one immutable order lifecycle event. The
// payload fields are flattened in; only the ones relevant to the type are set.
type orderEventDTO struct {
	At            time.Time `json:"at"`
	Type          string    `json:"type"`
	Source        string    `json:"source"`
	RejectCode    string    `json:"rejectCode,omitempty"`
	RejectScope   string    `json:"rejectScope,omitempty"`
	RejectPolicy  string    `json:"rejectPolicy,omitempty"`
	RejectReason  string    `json:"rejectReason,omitempty"`
	RejectDetails string    `json:"rejectDetails,omitempty"`
	FillQuantity  string    `json:"fillQuantity,omitempty"`
	FillPrice     string    `json:"fillPrice,omitempty"`
	FillLockPrice string    `json:"fillLockPrice,omitempty"`
	OrderID       int64     `json:"orderId"`
	ID            int64     `json:"id"`
}

// toOrderEventDTO maps a domain.OrderEvent onto the wire DTO.
func toOrderEventDTO(e domain.OrderEvent) orderEventDTO {
	return orderEventDTO{
		At:            e.At,
		Type:          string(e.Type),
		Source:        string(e.Source),
		RejectCode:    e.Payload.RejectCode,
		RejectScope:   e.Payload.RejectScope,
		RejectPolicy:  e.Payload.RejectPolicy,
		RejectReason:  e.Payload.RejectReason,
		RejectDetails: e.Payload.RejectDetails,
		FillQuantity:  e.Payload.FillQuantity,
		FillPrice:     e.Payload.FillPrice,
		FillLockPrice: e.Payload.FillLockPrice,
		OrderID:       e.OrderID,
		ID:            e.ID,
	}
}

// --- trade ------------------------------------------------------------------

// tradeDTO is the wire shape of one per-fill trade record. All monetary values
// are exact decimal strings passed through verbatim.
type tradeDTO struct {
	At         time.Time `json:"at"`
	Account    string    `json:"account"`
	BaseAsset  string    `json:"baseAsset"`
	QuoteAsset string    `json:"quoteAsset"`
	Side       string    `json:"side"`
	Quantity   string    `json:"quantity"`
	Price      string    `json:"price"`
	LockPrice  string    `json:"lockPrice"`
	Source     string    `json:"source"`
	OrderID    int64     `json:"orderId"`
	ID         int64     `json:"id"`
}

// toTradeDTO maps a domain.Trade onto the wire DTO.
func toTradeDTO(t domain.Trade) tradeDTO {
	return tradeDTO{
		At:         t.At,
		Account:    string(t.Account),
		BaseAsset:  t.BaseAsset,
		QuoteAsset: t.QuoteAsset,
		Side:       string(t.Side),
		Quantity:   t.Quantity,
		Price:      t.Price,
		LockPrice:  t.LockPrice,
		Source:     string(t.Source),
		OrderID:    t.OrderID,
		ID:         t.ID,
	}
}

// executionResultDTO is the wire shape of one execution-report outcome: the
// account blocks the engine recorded and the per-asset adjustment outcomes.
type executionResultDTO struct {
	Blocks   []executionBlockDTO     `json:"blocks"`
	Outcomes []adjustmentAcceptedDTO `json:"outcomes"`
}

// executionBlockDTO is one engine-recorded account block from a report.
type executionBlockDTO struct {
	Account string `json:"account"`
	Code    string `json:"code"`
	Reason  string `json:"reason"`
	Details string `json:"details"`
}

// toExecutionResultDTO maps an engine.ExecutionReportResult onto the wire DTO.
func toExecutionResultDTO(r engine.ExecutionReportResult) executionResultDTO {
	blocks := make([]executionBlockDTO, 0, len(r.Blocks))
	for _, b := range r.Blocks {
		blocks = append(blocks, executionBlockDTO{
			Account: string(b.Account),
			Code:    b.Code,
			Reason:  b.Reason,
			Details: b.Details,
		})
	}
	outcomes := make([]adjustmentAcceptedDTO, 0, len(r.Outcomes))
	for _, o := range r.Outcomes {
		outcomes = append(outcomes, adjustmentAcceptedDTO{
			BalanceDelta:   o.BalanceDelta,
			BalanceResult:  o.BalanceResult,
			HeldDelta:      o.HeldDelta,
			HeldResult:     o.HeldResult,
			IncomingDelta:  o.IncomingDelta,
			IncomingResult: o.IncomingResult,
		})
	}
	return executionResultDTO{Blocks: blocks, Outcomes: outcomes}
}

// --- overview / service -----------------------------------------------------

// overviewDTO is the wire shape of GET /overview.
type overviewDTO struct {
	Counts   countsDTO     `json:"counts"`
	Activity []activityDTO `json:"activity"`
}

// countsDTO is the headline tally on the overview.
type countsDTO struct {
	Accounts int `json:"accounts"`
	Groups   int `json:"groups"`
	Limits   int `json:"limits"`
}

// activityDTO is one recent-activity entry on the overview feed.
type activityDTO struct {
	At      time.Time `json:"at"`
	Source  string    `json:"source"`
	Kind    string    `json:"kind"`
	Ref     string    `json:"ref"`
	Summary string    `json:"summary"`
}

// toOverviewDTO maps a backend.Overview onto the wire DTO.
func toOverviewDTO(o backend.Overview) overviewDTO {
	activity := make([]activityDTO, 0, len(o.Activity))
	for _, a := range o.Activity {
		activity = append(activity, activityDTO{
			At:      a.At,
			Source:  string(a.Source),
			Kind:    string(a.Kind),
			Ref:     a.Ref,
			Summary: a.Summary,
		})
	}
	return overviewDTO{
		Counts: countsDTO{
			Accounts: o.Counts.Accounts,
			Groups:   o.Counts.Groups,
			Limits:   o.Counts.Limits,
		},
		Activity: activity,
	}
}

// serviceDTO is the wire shape of GET /service. The service is monolithic, so it
// reports one engine version and build profile and one database, not per-node
// detail.
type serviceDTO struct {
	Database serviceDatabaseDTO `json:"database"`
	Name     string             `json:"name"`
	Version  string             `json:"engineVersion"`
	Profile  string             `json:"engineBuildProfile"`
	Release  bool               `json:"release"`
}

// serviceDatabaseDTO is the database facet of serviceDTO.
type serviceDatabaseDTO struct {
	Path      string `json:"path"`
	Reachable bool   `json:"reachable"`
}

// toServiceDTO maps a backend.ServiceInfo onto the wire DTO.
func toServiceDTO(info backend.ServiceInfo) serviceDTO {
	return serviceDTO{
		Database: serviceDatabaseDTO{
			Path:      info.Database.Path,
			Reachable: info.Database.Reachable,
		},
		Name:    info.Name,
		Version: info.EngineVersion,
		Profile: info.EngineBuildProfile,
		Release: info.Release,
	}
}
