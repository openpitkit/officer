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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
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

// auditActionGroupDTO is the wire shape of one audit-action category paired with
// its actions, in domain canonical order.
type auditActionGroupDTO struct {
	Category string   `json:"category"`
	Actions  []string `json:"actions"`
}

// auditActionStrings maps a slice of domain.AuditAction onto plain strings,
// preserving order. The result is always a non-nil slice.
func auditActionStrings(actions []domain.AuditAction) []string {
	out := make([]string, 0, len(actions))
	for _, a := range actions {
		out = append(out, string(a))
	}
	return out
}

// --- MCP access control -----------------------------------------------------

// mcpCommandDTO is the wire shape of one MCP catalogue command paired with its
// current effective enabled state.
type mcpCommandDTO struct {
	Name             string `json:"name"`
	Title            string `json:"title"`
	AgentDescription string `json:"agentDescription"`
	Mutating         bool   `json:"mutating"`
	Protective       bool   `json:"protective"`
	Implemented      bool   `json:"implemented"`
	Enabled          bool   `json:"enabled"`
}

// toMcpCommandDTO maps a backend.McpCommand onto the wire DTO.
func toMcpCommandDTO(c backend.McpCommand) mcpCommandDTO {
	return mcpCommandDTO{
		Name:             c.Command.Name,
		Title:            c.Command.Title,
		AgentDescription: c.Command.AgentDescription,
		Mutating:         c.Command.Mutating,
		Protective:       c.Command.Protective,
		Implemented:      c.Command.Implemented,
		Enabled:          c.Enabled,
	}
}

// --- market data ------------------------------------------------------------

type marketDataDTO struct {
	Providers        []marketDataProviderDTO `json:"providers"`
	Instances        []marketDataInstanceDTO `json:"instances"`
	FreshnessSeconds int                     `json:"freshnessSeconds"`
	RestartRequired  bool                    `json:"restartRequired"`
}

type marketDataProviderDTO struct {
	Type  string `json:"type"`
	Title string `json:"title"`
}

type marketDataReferencesDTO struct {
	DocsURL    string `json:"docsUrl,omitempty"`
	SymbolsURL string `json:"symbolsUrl,omitempty"`
}

type marketDataInstanceDTO struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Label string `json:"label"`
	// Credentials is accepted on create requests but redacted from responses.
	Credentials     string                    `json:"credentials"`
	State           string                    `json:"state"`
	Error           string                    `json:"error,omitempty"`
	Enabled         bool                      `json:"enabled"`
	VerifiesSymbols bool                      `json:"verifiesSymbols"`
	SearchesSymbols bool                      `json:"searchesSymbols"`
	Settings        map[string]any            `json:"settings,omitempty"`
	Secrets         map[string]bool           `json:"secrets,omitempty"`
	References      *marketDataReferencesDTO  `json:"references,omitempty"`
	Instruments     []marketDataInstrumentDTO `json:"instruments,omitempty"`
	Diagnostics     []marketDataDiagnosticDTO `json:"diagnostics,omitempty"`
}

type marketDataCreateInstanceRequestDTO struct {
	Type        string `json:"type"`
	Label       string `json:"label"`
	Credentials string `json:"credentials"`
	Enabled     bool   `json:"enabled"`
}

type marketDataUpdateInstanceSettingsRequestDTO struct {
	Label       string `json:"label"`
	Credentials string `json:"credentials"`
}

// marketDataSymbolVerificationDTO is the body of POST
// .../instances/{id}/verify-symbol. Supported is false when the provider cannot
// verify symbols; Details carries optional provider metadata for the matched
// symbol. Suggestion carries a case-folded catalogue variant when the symbol was
// not found as typed.
type marketDataSymbolVerificationDTO struct {
	Supported  bool   `json:"supported"`
	Exists     bool   `json:"exists"`
	Suggestion string `json:"suggestion,omitempty"`
	Details    string `json:"details,omitempty"`
}

// marketDataSymbolMatchDTO is one contract returned by POST
// .../instances/{id}/search-symbols. Name is the resolved long name; every
// field except symbol/secType is omitted when empty.
type marketDataSymbolMatchDTO struct {
	Symbol                       string `json:"symbol"`
	Name                         string `json:"name,omitempty"`
	SecType                      string `json:"secType"`
	Exchange                     string `json:"exchange,omitempty"`
	PrimaryExchange              string `json:"primaryExchange,omitempty"`
	Currency                     string `json:"currency,omitempty"`
	LastTradeDateOrContractMonth string `json:"lastTradeDateOrContractMonth,omitempty"`
	Right                        string `json:"right,omitempty"`
	Multiplier                   string `json:"multiplier,omitempty"`
	LocalSymbol                  string `json:"localSymbol,omitempty"`
	TradingClass                 string `json:"tradingClass,omitempty"`
	ConID                        string `json:"conId,omitempty"`
	Strike                       string `json:"strike,omitempty"`
}

type marketDataDiagnosticActionDTO struct {
	Type   string `json:"type"`
	Target string `json:"target,omitempty"`
}

type marketDataDiagnosticDTO struct {
	Level       string                          `json:"level"`
	Code        string                          `json:"code"`
	Kind        string                          `json:"kind"`
	Title       string                          `json:"title"`
	Detail      string                          `json:"detail"`
	Remediation string                          `json:"remediation,omitempty"`
	Instrument  string                          `json:"instrument,omitempty"`
	Actions     []marketDataDiagnosticActionDTO `json:"actions,omitempty"`
	At          string                          `json:"at"`
}

type marketDataInstrumentDTO struct {
	InstanceID     string `json:"instanceId,omitempty"`
	ExternalSymbol string `json:"externalSymbol"`
	BaseAsset      string `json:"baseAsset"`
	QuoteAsset     string `json:"quoteAsset"`
	ManualPrice    string `json:"manualPrice"`
	// UpdateIntervalMs is the elapsed time, in milliseconds, between the two most
	// recent ticks of this instrument's quote. Omitted while it is unknown (fewer
	// than two ticks since the last (re)subscribe). It is a fixed measurement,
	// not an age that grows between ticks.
	UpdateIntervalMs int                 `json:"updateIntervalMs,omitempty"`
	Enabled          bool                `json:"enabled"`
	Stale            bool                `json:"stale"`
	Quote            *marketDataQuoteDTO `json:"quote,omitempty"`
}

type marketDataQuoteDTO struct {
	AsOf       string `json:"asOf"`
	ReceivedAt string `json:"receivedAt"`
	Mark       string `json:"mark"`
	Bid        string `json:"bid"`
	Ask        string `json:"ask"`
}

func toMarketDataDTO(status backend.MarketDataStatus) marketDataDTO {
	providers := make([]marketDataProviderDTO, 0, len(status.Providers))
	for _, provider := range status.Providers {
		providers = append(providers, marketDataProviderDTO{
			Type:  provider.Type,
			Title: provider.Title,
		})
	}
	instances := make([]marketDataInstanceDTO, 0, len(status.Instances))
	for _, instance := range status.Instances {
		instances = append(instances, toMarketDataInstanceDTO(instance))
	}
	return marketDataDTO{
		Providers:        providers,
		Instances:        instances,
		FreshnessSeconds: status.FreshnessSeconds,
		RestartRequired:  status.RestartRequired,
	}
}

func toMarketDataInstanceDTO(status backend.MarketDataInstanceStatus) marketDataInstanceDTO {
	instruments := make([]marketDataInstrumentDTO, 0, len(status.Instruments))
	for _, instrument := range status.Instruments {
		instruments = append(instruments, toMarketDataInstrumentDTO(instrument))
	}
	diagnostics := make([]marketDataDiagnosticDTO, 0, len(status.Diagnostics))
	for _, diag := range status.Diagnostics {
		var actions []marketDataDiagnosticActionDTO
		if len(diag.Actions) > 0 {
			actions = make([]marketDataDiagnosticActionDTO, 0, len(diag.Actions))
			for _, a := range diag.Actions {
				actions = append(actions, marketDataDiagnosticActionDTO{
					Type:   a.Type,
					Target: a.Target,
				})
			}
		}
		diagnostics = append(diagnostics, marketDataDiagnosticDTO{
			Level:       diag.Level,
			Code:        diag.Code,
			Kind:        diag.Kind,
			Title:       diag.Title,
			Detail:      diag.Detail,
			Remediation: diag.Remediation,
			Instrument:  diag.Instrument,
			Actions:     actions,
			At:          diag.At.Format(time.RFC3339Nano),
		})
	}
	var refs *marketDataReferencesDTO
	if r := status.References; r != nil && (r.DocsURL != "" || r.SymbolsURL != "") {
		refs = &marketDataReferencesDTO{
			DocsURL:    r.DocsURL,
			SymbolsURL: r.SymbolsURL,
		}
	}
	return marketDataInstanceDTO{
		ID:              status.Instance.ID,
		Type:            status.Instance.Type,
		Label:           status.Instance.Label,
		Credentials:     "",
		State:           status.State,
		Error:           status.Error,
		Enabled:         status.Instance.Enabled,
		VerifiesSymbols: status.VerifiesSymbols,
		SearchesSymbols: status.SearchesSymbols,
		Settings:        marketDataSafeSettings(status.Instance),
		Secrets:         marketDataSecretState(status.Instance),
		References:      refs,
		Instruments:     instruments,
		Diagnostics:     diagnostics,
	}
}

func marketDataSafeSettings(instance domain.MarketDataInstance) map[string]any {
	credentials := marketDataCredentialsObject(instance.Credentials)
	if len(credentials) == 0 {
		return nil
	}
	settings := make(map[string]any)
	switch instance.Type {
	case domain.MarketDataProviderIB:
		copyStringSetting(settings, credentials, "host")
		copyNumberSetting(settings, credentials, "port")
		copyNumberSetting(settings, credentials, "clientId")
		copyStringSetting(settings, credentials, "marketDataType")
		copyNumberSetting(settings, credentials, "marketDataType")
		copyObjectSetting(settings, credentials, "contracts")
	case domain.MarketDataProviderBybit:
		copyStringSetting(settings, credentials, "category")
	case domain.MarketDataProviderOANDA:
		copyStringSetting(settings, credentials, "accountID")
		copyStringSetting(settings, credentials, "environment")
	}
	if len(settings) == 0 {
		return nil
	}
	return settings
}

func marketDataSecretState(instance domain.MarketDataInstance) map[string]bool {
	credentials := marketDataCredentialsObject(instance.Credentials)
	if len(credentials) == 0 {
		return nil
	}
	secrets := make(map[string]bool)
	switch instance.Type {
	case domain.MarketDataProviderAlpaca:
		secrets["apiKey"] = stringCredentialPresent(credentials, "apiKey") ||
			stringCredentialPresent(credentials, "key")
		secrets["apiSecret"] = stringCredentialPresent(credentials, "apiSecret") ||
			stringCredentialPresent(credentials, "secret")
	case domain.MarketDataProviderOANDA, domain.MarketDataProviderFinnhub:
		secrets["token"] = stringCredentialPresent(credentials, "token")
	}
	for _, present := range secrets {
		if present {
			return secrets
		}
	}
	return nil
}

func marketDataCredentialsObject(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var credentials map[string]any
	if err := json.Unmarshal([]byte(raw), &credentials); err != nil {
		return nil
	}
	return credentials
}

func copyStringSetting(dst, src map[string]any, key string) {
	value, ok := src[key].(string)
	if ok && strings.TrimSpace(value) != "" {
		dst[key] = value
	}
}

func copyNumberSetting(dst, src map[string]any, key string) {
	value, ok := src[key].(float64)
	if ok {
		dst[key] = value
	}
}

// copyObjectSetting copies a non-empty nested object/map value verbatim. It is
// used for non-secret structured settings (the IB contract overrides), which
// carry no credentials.
func copyObjectSetting(dst, src map[string]any, key string) {
	if v, ok := src[key].(map[string]any); ok && len(v) > 0 {
		dst[key] = v
	}
}

func stringCredentialPresent(credentials map[string]any, key string) bool {
	value, ok := credentials[key].(string)
	return ok && strings.TrimSpace(value) != ""
}

func toMarketDataSymbolVerificationDTO(
	v backend.MarketDataSymbolVerification,
) marketDataSymbolVerificationDTO {
	return marketDataSymbolVerificationDTO{
		Supported:  v.Supported,
		Exists:     v.Exists,
		Suggestion: v.Suggestion,
		Details:    v.Details,
	}
}

func toMarketDataSymbolMatchDTOs(
	in []backend.MarketDataSymbolMatch,
) []marketDataSymbolMatchDTO {
	out := make([]marketDataSymbolMatchDTO, 0, len(in))
	for _, match := range in {
		out = append(out, marketDataSymbolMatchDTO{
			Symbol:                       match.Symbol,
			Name:                         match.Name,
			SecType:                      match.SecType,
			Exchange:                     match.Exchange,
			PrimaryExchange:              match.PrimaryExchange,
			Currency:                     match.Currency,
			LastTradeDateOrContractMonth: match.LastTradeDateOrContractMonth,
			Right:                        match.Right,
			Multiplier:                   match.Multiplier,
			LocalSymbol:                  match.LocalSymbol,
			TradingClass:                 match.TradingClass,
			ConID:                        match.ConID,
			Strike:                       match.Strike,
		})
	}
	return out
}

func toMarketDataInstrumentDTO(
	status backend.MarketDataInstrumentStatus,
) marketDataInstrumentDTO {
	var intervalMs int
	if status.UpdateInterval != nil {
		intervalMs = int(status.UpdateInterval.Milliseconds())
	}
	return marketDataInstrumentDTO{
		InstanceID:       status.Instrument.InstanceID,
		ExternalSymbol:   status.Instrument.ExternalSymbol,
		BaseAsset:        status.Instrument.BaseAsset,
		QuoteAsset:       status.Instrument.QuoteAsset,
		ManualPrice:      status.Instrument.ManualPrice,
		UpdateIntervalMs: intervalMs,
		Enabled:          status.Instrument.Enabled,
		Stale:            status.Stale,
		Quote:            toMarketDataQuoteDTO(status.Quote),
	}
}

func toMarketDataQuoteDTO(quote *domain.MarketDataQuote) *marketDataQuoteDTO {
	if quote == nil {
		return nil
	}
	return &marketDataQuoteDTO{
		AsOf:       quote.AsOf.Format(time.RFC3339Nano),
		ReceivedAt: quote.ReceivedAt.Format(time.RFC3339Nano),
		Mark:       quote.Mark,
		Bid:        quote.Bid,
		Ask:        quote.Ask,
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
	RealizedPnl       string    `json:"realizedPnl"`
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
		RealizedPnl:       b.RealizedPnl,
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

// orderApprovalDTO is the wire shape of an order's persisted signed approval
// envelope. Token is the exact base64url envelope bytes; the rest is the
// envelope metadata. Signed reports whether the envelope carries an Ed25519
// signature (alg "ed25519") versus an eSign-off envelope (alg "none").
type orderApprovalDTO struct {
	Token     string `json:"token"`
	KeyID     string `json:"keyId"`
	Alg       string `json:"alg"`
	Mode      string `json:"mode"`
	IssuedAt  string `json:"issuedAt"`
	ExpiresAt string `json:"expiresAt"`
	Signed    bool   `json:"signed"`
}

// toOrderApprovalDTO maps an order's persisted envelope onto the wire DTO, or
// returns nil when the order carries no envelope (approval token empty).
func toOrderApprovalDTO(o domain.Order) *orderApprovalDTO {
	if o.ApprovalToken == "" {
		return nil
	}
	return &orderApprovalDTO{
		Token:     o.ApprovalToken,
		KeyID:     o.ApprovalKeyID,
		Alg:       o.ApprovalAlg,
		Mode:      o.ApprovalMode,
		IssuedAt:  o.ApprovalIssuedAt,
		ExpiresAt: o.ApprovalExpiresAt,
		Signed:    o.ApprovalAlg == "ed25519",
	}
}

// --- order check ------------------------------------------------------------

// orderRejectDTO is the wire shape of one engine pre-trade reject from a
// non-mutating order check.
type orderRejectDTO struct {
	Code    string `json:"code"`
	Scope   string `json:"scope"`
	Policy  string `json:"policy"`
	Reason  string `json:"reason"`
	Details string `json:"details"`
}

// checkResultDTO is the wire shape of a non-mutating order-check outcome: the
// would-be lock prices on pass, or the structured rejects and any would-be
// account block on reject. Monetary values are exact decimal strings.
type checkResultDTO struct {
	WouldBlock      *executionBlockDTO `json:"wouldBlock"`
	Rejects         []orderRejectDTO   `json:"rejects"`
	WouldLockPrices []string           `json:"wouldLockPrices"`
	Passed          bool               `json:"passed"`
}

// toCheckResultDTO maps a domain.CheckResult onto the wire DTO. It reuses the
// execution-block shape for the would-be block.
func toCheckResultDTO(r domain.CheckResult) checkResultDTO {
	rejects := make([]orderRejectDTO, 0, len(r.Rejects))
	for _, rej := range r.Rejects {
		rejects = append(rejects, orderRejectDTO{
			Code:    rej.Code,
			Scope:   rej.Scope,
			Policy:  rej.Policy,
			Reason:  rej.Reason,
			Details: rej.Details,
		})
	}
	prices := r.WouldLockPrices
	if prices == nil {
		prices = []string{}
	}
	var block *executionBlockDTO
	if r.WouldBlock != nil {
		block = &executionBlockDTO{
			Account: string(r.WouldBlock.Account),
			Code:    r.WouldBlock.Code,
			Reason:  r.WouldBlock.Reason,
			Details: r.WouldBlock.Details,
		}
	}
	return checkResultDTO{
		WouldBlock:      block,
		Rejects:         rejects,
		WouldLockPrices: prices,
		Passed:          r.Passed,
	}
}

// --- order event ------------------------------------------------------------

// orderEventDTO is the wire shape of one immutable order lifecycle event. The
// payload fields are flattened in; only the ones relevant to the type are set.
// Reject fields can also be present on fill events that caused an account block.
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
	Blocks   []executionBlockDTO   `json:"blocks"`
	Outcomes []executionOutcomeDTO `json:"outcomes"`
}

// executionOutcomeDTO is one per-asset balance effect of a fill, tagged with its
// asset so the base and quote legs of a spot fill can be told apart.
type executionOutcomeDTO struct {
	Asset          string `json:"asset"`
	BalanceDelta   string `json:"balanceDelta"`
	BalanceResult  string `json:"balanceResult"`
	HeldDelta      string `json:"heldDelta"`
	HeldResult     string `json:"heldResult"`
	IncomingDelta  string `json:"incomingDelta"`
	IncomingResult string `json:"incomingResult"`
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
	outcomes := make([]executionOutcomeDTO, 0, len(r.Outcomes))
	for _, o := range r.Outcomes {
		outcomes = append(outcomes, executionOutcomeDTO{
			Asset:          o.Asset,
			BalanceDelta:   o.Outcome.BalanceDelta,
			BalanceResult:  o.Outcome.BalanceResult,
			HeldDelta:      o.Outcome.HeldDelta,
			HeldResult:     o.Outcome.HeldResult,
			IncomingDelta:  o.Outcome.IncomingDelta,
			IncomingResult: o.Outcome.IncomingResult,
		})
	}
	return executionResultDTO{Blocks: blocks, Outcomes: outcomes}
}

// --- signing keys -----------------------------------------------------------

// signingKeyDTO is the wire shape of one Ed25519 signing key entry. Private
// material is NEVER present.
type signingKeyDTO struct {
	KeyID       string `json:"keyId"`
	Alg         string `json:"alg"`
	CreatedAt   string `json:"createdAt"`
	Fingerprint string `json:"fingerprint"`
	Active      bool   `json:"active"`
}

// signingKeyImportRequestDTO is the body of POST /signing/keys/import.
type signingKeyImportRequestDTO struct {
	// Key is the raw key material in the given format. Write-only on the wire.
	Key    string `json:"key"`
	Format string `json:"format"`
}

// signingConfigDTO is the body of GET/PUT /signing/config.
type signingConfigDTO struct {
	NoESign bool `json:"noESign"`
}

// publicKeyDTO is the body of GET /signing/keys/active/public.
type publicKeyDTO struct {
	PublicKey string `json:"publicKey"`
}

// submitOrderTokenRequestDTO is the body of POST /orders/{id}/submit.
type submitOrderTokenRequestDTO struct {
	Mode string `json:"mode"`
}

// approvalTokenDTO is the response of POST /orders/{id}/submit.
type approvalTokenDTO struct {
	Token     string `json:"token"`
	KeyID     string `json:"keyId"`
	ExpiresAt string `json:"expiresAt"`
	OrderID   int64  `json:"orderId"`
}

// confirmExecutionRequestDTO is the body of POST /orders/{id}/confirm.
type confirmExecutionRequestDTO struct {
	Token string `json:"token"`
	Force bool   `json:"force"`
}

// cancelOrderRequestDTO is the body of POST /orders/{id}/cancel.
type cancelOrderRequestDTO struct {
	Token  string `json:"token"`
	Reason string `json:"reason"`
	Force  bool   `json:"force"`
}

// toSigningKeyDTO maps a domain.SigningKey onto the wire DTO. It NEVER copies
// PrivateKey. The fingerprint is the first 8 bytes of the SHA-256 of the
// public key, hex-encoded — the same derivation used by the signing package.
func toSigningKeyDTO(k domain.SigningKey) signingKeyDTO {
	fp := ""
	if len(k.PublicKey) > 0 {
		sum := sha256.Sum256(k.PublicKey)
		fp = hex.EncodeToString(sum[:8])
	}
	return signingKeyDTO{
		KeyID:       k.KeyID,
		Alg:         k.Alg,
		CreatedAt:   k.CreatedAt.UTC().Format(time.RFC3339Nano),
		Fingerprint: fp,
		Active:      k.Active,
	}
}

// --- overview / service -----------------------------------------------------

// overviewDTO is the wire shape of GET /overview.
type overviewDTO struct {
	Counts   countsDTO     `json:"counts"`
	Activity []activityDTO `json:"activity"`
}

// countsDTO is the headline tally on the overview.
type countsDTO struct {
	Accounts       int `json:"accounts"`
	AccountsActive int `json:"accountsActive"`
	Groups         int `json:"groups"`
	GroupsActive   int `json:"groupsActive"`
	Limits         int `json:"limits"`
	OrdersToday    int `json:"ordersToday"`
	OrdersTotal    int `json:"ordersTotal"`
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
			Accounts:       o.Counts.Accounts,
			AccountsActive: o.Counts.AccountsActive,
			Groups:         o.Counts.Groups,
			GroupsActive:   o.Counts.GroupsActive,
			Limits:         o.Counts.Limits,
			OrdersToday:    o.Counts.OrdersToday,
			OrdersTotal:    o.Counts.OrdersTotal,
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

// serviceLogsDTO is the body of GET /service/logs: the buffered log tail oldest
// line first, with the line count. Lines is always a JSON array (never null) so
// the dashboard can render it without a presence check.
type serviceLogsDTO struct {
	Lines []string `json:"lines"`
	Count int      `json:"count"`
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
