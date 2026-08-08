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

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
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

// accountDTO is the wire shape of a single account. An account is a dictionary
// record: its public handle is the code, paired with a mutable title.
// The engine account id and the store surrogate id are never serialized.
//
// The blocked/blockReason/blockSource triple is the EFFECTIVE kill-switch
// state: the account's own block joined with its group's, so a consumer that
// reads only blocked gets the answer to "can this account trade right now".
// The per-tier fields carry the two independent underlying blocks.
type accountDTO struct {
	Code               string             `json:"code"`
	Title              string             `json:"title"`
	Pnl                string             `json:"pnl"`
	PnlHaltReason      string             `json:"pnlHaltReason"`
	Group              string             `json:"group"`
	Currency           string             `json:"currency"`
	EffectiveCurrency  string             `json:"effectiveCurrency"`
	CurrencyOrigin     string             `json:"currencyOrigin"`
	CurrencyCascade    currencyCascadeDTO `json:"currencyCascade"`
	Notes              string             `json:"notes"`
	PositionCount      int                `json:"positionCount"`
	BlockReason        string             `json:"blockReason"`
	BlockSource        string             `json:"blockSource"`
	AccountBlockReason string             `json:"accountBlockReason"`
	GroupBlockReason   string             `json:"groupBlockReason"`
	Blocked            bool               `json:"blocked"`
	AccountBlocked     bool               `json:"accountBlocked"`
	GroupBlocked       bool               `json:"groupBlocked"`
}

type currencyCascadeDTO struct {
	Account string `json:"account"`
	Group   string `json:"group"`
	Default string `json:"default"`
}

// assetDTO is the wire shape of a single asset dictionary record. Its public
// code is operator-chosen and renameable; the store surrogate id is never
// serialized.
type assetDTO struct {
	Code       string `json:"code"`
	Title      string `json:"title"`
	AssetClass string `json:"assetClass"`
}

// assetClassDTO is the wire shape of a single asset-class dictionary record. A
// class's public handle is its code; the store surrogate id is never serialized.
type assetClassDTO struct {
	Code       string `json:"code"`
	Title      string `json:"title"`
	Notes      string `json:"notes"`
	AssetCount int    `json:"assetCount"`
}

// rateLimitDTO is the wire shape of a rate-limit barrier.
type rateLimitDTO struct {
	Scope     string `json:"scope"`
	Account   string `json:"account"`
	Asset     string `json:"asset"`
	WindowMs  int64  `json:"windowMs"`
	MaxOrders uint64 `json:"maxOrders"`
}

// orderSizeLimitDTO is the wire shape of an order-size barrier. The ceilings are
// exact decimal strings; an unset ceiling is the empty string.
type orderSizeLimitDTO struct {
	Scope       string `json:"scope"`
	Account     string `json:"account"`
	Asset       string `json:"asset"`
	MaxQuantity string `json:"maxQuantity"`
	MaxNotional string `json:"maxNotional"`
}

// spotFundsPnlBoundsLimitDTO is the wire shape of a SpotFunds self-computed
// P&L-bounds barrier. P&L is always in the account currency, so the barrier
// has no separate currency or asset axis.
type spotFundsPnlBoundsLimitDTO struct {
	Scope        string `json:"scope"`
	Account      string `json:"account"`
	AccountGroup string `json:"accountGroup"`
	LowerBound   string `json:"lowerBound"`
	UpperBound   string `json:"upperBound"`
}

// accountLimitsDTO is the per-policy view of an account's typed barriers,
// returned by the account-state and limit-list endpoints. Each slice is always
// a non-nil JSON array.
type accountLimitsDTO struct {
	RateLimits               []rateLimitDTO               `json:"rateLimits"`
	OrderSizeLimits          []orderSizeLimitDTO          `json:"orderSizeLimits"`
	SpotFundsPnlBoundsLimits []spotFundsPnlBoundsLimitDTO `json:"spotFundsPnlBoundsLimits"`
}

// policyRateValuesDTO carries the rate-limit-specific values of a policy row.
type policyRateValuesDTO struct {
	WindowMs  int64  `json:"windowMs"`
	MaxOrders uint64 `json:"maxOrders"`
}

// policyOrderSizeValuesDTO carries the order-size-specific values of a policy
// row. The ceilings are exact decimal strings; an unset ceiling is empty.
type policyOrderSizeValuesDTO struct {
	MaxQuantity string `json:"maxQuantity"`
	MaxNotional string `json:"maxNotional"`
}

// policyPnlBoundsValuesDTO carries the P&L-bounds-specific values of a policy
// row. The bounds are exact decimal strings; an unset value is empty.
type policyPnlBoundsValuesDTO struct {
	LowerBound string `json:"lowerBound"`
	UpperBound string `json:"upperBound"`
}

// policyValuesDTO is the heterogeneous value payload of a policy row: exactly
// one of the four sub-objects is present, matching the row's kind. The common
// scope/account/asset axes live on policyDTO, not here.
type policyValuesDTO struct {
	Rate               *policyRateValuesDTO      `json:"rate,omitempty"`
	OrderSize          *policyOrderSizeValuesDTO `json:"orderSize,omitempty"`
	SpotFundsPnlBounds *policyPnlBoundsValuesDTO `json:"spotFundsPnlBounds,omitempty"`
}

// policyDTO is the wire shape of one typed barrier flattened into the unified
// policy list. The kind discriminator selects which member of values is set;
// the scope/account/accountGroup/asset axes are shared across all kinds.
type policyDTO struct {
	Kind         string          `json:"kind"`
	Scope        string          `json:"scope"`
	Account      string          `json:"account"`
	AccountGroup string          `json:"accountGroup"`
	Asset        string          `json:"asset"`
	Values       policyValuesDTO `json:"values"`
}

// auditDTO is the wire shape of a single audit row. An audit row is a machine
// record: its public handle is the opaque id; no surrogate id appears.
type auditDTO struct {
	At           time.Time `json:"at"`
	ID           string    `json:"id"`
	Actor        string    `json:"actor"`
	ActorTitle   string    `json:"actorTitle"`
	Action       string    `json:"action"`
	Account      string    `json:"account"`
	AccountTitle string    `json:"accountTitle"`
	Asset        string    `json:"asset"`
	// Group is the structured group handle of a group action, so a reader
	// selects a group's rows by identity instead of matching the free-form
	// detail text, which a crafted group code can spoof.
	Group  string `json:"group"`
	Detail string `json:"detail"`
	Source string `json:"source"`
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

// toAccountDTO maps a domain.Account onto the wire DTO. The account's public
// handle is its code; the engine account id is never serialized. block is the
// account joined with its group, resolved by the caller, which owns the group
// read; the account row alone carries only the account's own latched flag.
func toAccountDTO(a domain.Account, block domain.AccountBlockState) accountDTO {
	return accountDTO{
		Code:              string(a.Code),
		Title:             a.Title,
		Pnl:               a.Pnl,
		PnlHaltReason:     string(a.PnlHaltReason),
		Group:             a.GroupCode,
		Currency:          a.Currency,
		EffectiveCurrency: a.EffectiveCurrency,
		CurrencyOrigin:    a.CurrencyOrigin,
		CurrencyCascade: currencyCascadeDTO{
			Account: a.Currency,
			Group:   a.GroupCurrency,
			Default: a.DefaultCurrency,
		},
		Notes:              a.Notes,
		BlockReason:        block.Reason,
		BlockSource:        string(block.Source),
		AccountBlockReason: block.AccountReason,
		GroupBlockReason:   block.GroupReason,
		Blocked:            block.Blocked,
		AccountBlocked:     block.AccountBlocked,
		GroupBlocked:       block.GroupBlocked,
	}
}

// toAccountRowDTO maps a list row onto the account wire DTO.
func toAccountRowDTO(
	row store.AccountListRow, block domain.AccountBlockState,
) accountDTO {
	dto := toAccountDTO(row.Account, block)
	dto.PositionCount = row.PositionCount
	return dto
}

// toAssetDTO maps a domain.Asset onto the wire DTO.
func toAssetDTO(asset domain.Asset) assetDTO {
	return assetDTO{
		Code:       asset.Code,
		Title:      asset.Title,
		AssetClass: asset.AssetClass,
	}
}

// toAssetClassDTO maps a domain.AssetClass onto the wire DTO.
func toAssetClassDTO(class domain.AssetClass) assetClassDTO {
	return assetClassDTO{
		Code:  class.Code,
		Title: class.Title,
		Notes: class.Notes,
	}
}

// toAssetClassRowDTO maps a list row onto the asset-class wire DTO.
func toAssetClassRowDTO(row store.AssetClassListRow) assetClassDTO {
	dto := toAssetClassDTO(row.Class)
	dto.AssetCount = row.AssetCount
	return dto
}

// toRateLimitDTO maps a domain.LimitRate onto the wire DTO.
func toRateLimitDTO(l domain.LimitRate) rateLimitDTO {
	return rateLimitDTO{
		Scope:     l.Scope,
		Account:   string(l.Account),
		Asset:     l.Asset,
		WindowMs:  l.Window.Milliseconds(),
		MaxOrders: l.MaxOrders,
	}
}

// toOrderSizeLimitDTO maps a domain.LimitOrderSize onto the wire DTO.
func toOrderSizeLimitDTO(l domain.LimitOrderSize) orderSizeLimitDTO {
	return orderSizeLimitDTO{
		Scope:       l.Scope,
		Account:     string(l.Account),
		Asset:       l.Asset,
		MaxQuantity: l.MaxQuantity,
		MaxNotional: l.MaxNotional,
	}
}

// toSpotFundsPnlBoundsLimitDTO maps a domain.LimitSpotFundsPnlBounds onto the
// wire DTO.
func toSpotFundsPnlBoundsLimitDTO(
	l domain.LimitSpotFundsPnlBounds,
) spotFundsPnlBoundsLimitDTO {
	return spotFundsPnlBoundsLimitDTO{
		Scope:        l.Scope,
		Account:      string(l.Account),
		AccountGroup: l.AccountGroup,
		LowerBound:   l.LowerBound,
		UpperBound:   l.UpperBound,
	}
}

// toAccountLimitsDTO maps the per-policy typed barriers onto the wire DTO. Each
// slice is a non-nil JSON array.
func toAccountLimitsDTO(limits node.AccountLimits) accountLimitsDTO {
	rates := make([]rateLimitDTO, 0, len(limits.RateLimits))
	for _, l := range limits.RateLimits {
		rates = append(rates, toRateLimitDTO(l))
	}
	sizes := make([]orderSizeLimitDTO, 0, len(limits.OrderSizeLimits))
	for _, l := range limits.OrderSizeLimits {
		sizes = append(sizes, toOrderSizeLimitDTO(l))
	}
	spotFundsPnls := make(
		[]spotFundsPnlBoundsLimitDTO,
		0,
		len(limits.SpotFundsPnlBoundsLimits),
	)
	for _, l := range limits.SpotFundsPnlBoundsLimits {
		spotFundsPnls = append(spotFundsPnls, toSpotFundsPnlBoundsLimitDTO(l))
	}
	return accountLimitsDTO{
		RateLimits:               rates,
		OrderSizeLimits:          sizes,
		SpotFundsPnlBoundsLimits: spotFundsPnls,
	}
}

// toPolicyRowDTO maps a unified policy list row onto the wire DTO. It carries
// the shared scope/account/asset axes and the one value payload that matches the
// row's kind, rendered with the same per-kind fields as toAccountLimitsDTO.
func toPolicyRowDTO(row store.PolicyListRow) policyDTO {
	dto := policyDTO{
		Kind:         string(row.Kind),
		Scope:        row.Scope,
		Account:      string(row.Account),
		AccountGroup: row.AccountGroup,
		Asset:        row.Asset,
	}
	switch {
	case row.Rate != nil:
		dto.Values.Rate = &policyRateValuesDTO{
			WindowMs:  row.Rate.Window.Milliseconds(),
			MaxOrders: row.Rate.MaxOrders,
		}
	case row.OrderSize != nil:
		dto.Values.OrderSize = &policyOrderSizeValuesDTO{
			MaxQuantity: row.OrderSize.MaxQuantity,
			MaxNotional: row.OrderSize.MaxNotional,
		}
	case row.SpotFundsPnlBounds != nil:
		dto.Values.SpotFundsPnlBounds = &policyPnlBoundsValuesDTO{
			LowerBound: row.SpotFundsPnlBounds.LowerBound,
			UpperBound: row.SpotFundsPnlBounds.UpperBound,
		}
	}
	return dto
}

// toAuditDTO maps a domain.AuditRow onto the wire DTO. The audit row's public
// handle is its opaque id; no surrogate id is serialized.
func toAuditDTO(row domain.AuditRow) auditDTO {
	return auditDTO{
		ID:           row.ExternalID.String(),
		At:           row.At,
		Actor:        row.Actor,
		ActorTitle:   row.ActorTitle,
		Action:       string(row.Action),
		Account:      string(row.Account),
		AccountTitle: row.AccountTitle,
		Asset:        row.Asset,
		Group:        row.Group,
		Detail:       row.Detail,
		Source:       string(row.Source),
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
	// ID is the instance's opaque public handle (a machine record). The
	// store surrogate id is never serialized.
	ID string `json:"id"`
	// Provider is the provider discriminator (e.g. "ib", "binance").
	Provider string `json:"provider"`
	Label    string `json:"label"`
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

// marketDataCreateInstanceRequestDTO is the body of POST
// /market-data/instances. ID is the optional caller-supplied opaque
// public handle for the instance; when omitted the server generates and returns
// one. A supplied id is accepted verbatim and
// must be unique (a duplicate is a 409); it is never a surrogate id.
type marketDataCreateInstanceRequestDTO struct {
	ID          string `json:"id,omitempty"`
	Provider    string `json:"provider"`
	Label       string `json:"label"`
	Credentials string `json:"credentials"`
	Enabled     *bool  `json:"enabled"`
}

type marketDataUpdateInstanceSettingsRequestDTO struct {
	Label       string `json:"label"`
	Credentials string `json:"credentials"`
}

type marketDataUpsertInstrumentRequestDTO struct {
	ExternalSymbol string  `json:"externalSymbol"`
	BaseAsset      string  `json:"baseAsset"`
	QuoteAsset     string  `json:"quoteAsset"`
	ManualPrice    *string `json:"manualPrice"`
	Enabled        *bool   `json:"enabled"`
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
	// InstanceExternalID is the owning instance's opaque public handle; omitted
	// in request bodies (taken from the path) and set in responses.
	InstanceExternalID string `json:"instanceId,omitempty"`
	ExternalSymbol     string `json:"externalSymbol"`
	BaseAsset          string `json:"baseAsset"`
	QuoteAsset         string `json:"quoteAsset"`
	ManualPrice        string `json:"manualPrice"`
	// UpdateIntervalMs is the elapsed time, in milliseconds, between the two most
	// recent ticks of this instrument's quote. Omitted while it is unknown (fewer
	// than two ticks since the last (re)subscribe). It is a fixed measurement,
	// not an age that grows between ticks.
	UpdateIntervalMs int                 `json:"updateIntervalMs,omitempty"`
	Enabled          bool                `json:"enabled"`
	SyntheticInverse bool                `json:"syntheticInverse"`
	Stale            bool                `json:"stale"`
	Quote            *marketDataQuoteDTO `json:"quote,omitempty"`
	InverseQuote     *marketDataQuoteDTO `json:"inverseQuote,omitempty"`
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
		ID:              status.Instance.ExternalID.String(),
		Provider:        status.Instance.Provider,
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
	switch instance.Provider {
	case domain.MarketDataProviderIB:
		copyStringSetting(settings, credentials, "host")
		copyNumberSetting(settings, credentials, "port")
		copyNumberSetting(settings, credentials, "clientId")
		copyStringSetting(settings, credentials, "marketDataType")
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
	switch instance.Provider {
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
		InstanceExternalID: status.Instrument.Instance.String(),
		ExternalSymbol:     status.Instrument.ExternalSymbol,
		BaseAsset:          status.Instrument.BaseAsset,
		QuoteAsset:         status.Instrument.QuoteAsset,
		ManualPrice:        status.Instrument.ManualPrice,
		UpdateIntervalMs:   intervalMs,
		Enabled:            status.Instrument.Enabled,
		SyntheticInverse:   status.SyntheticInverse,
		Stale:              status.Stale,
		Quote:              toMarketDataQuoteDTO(status.Quote),
		InverseQuote:       toMarketDataQuoteDTO(status.InverseQuote),
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

// groupDTO is the wire shape of a single account group. A group is a dictionary
// record: its public handle is the code, paired with a mutable title.
// The engine group id and the store surrogate id are never serialized.
type groupDTO struct {
	Code          string `json:"code"`
	Title         string `json:"title"`
	Currency      string `json:"currency"`
	Notes         string `json:"notes"`
	AccountCount  int    `json:"accountCount"`
	PositionCount int    `json:"positionCount"`
	BlockReason   string `json:"blockReason"`
	Blocked       bool   `json:"blocked"`
}

// toGroupDTO maps a domain.AccountGroup onto the wire DTO. The group's public
// handle is its code; the engine group id is never serialized.
func toGroupDTO(g domain.AccountGroup) groupDTO {
	return groupDTO{
		Code:        g.Code,
		Title:       g.Title,
		Currency:    g.Currency,
		Notes:       g.Notes,
		BlockReason: g.BlockReason,
		Blocked:     g.Blocked,
	}
}

// toGroupRowDTO maps a list row onto the group wire DTO.
func toGroupRowDTO(row store.GroupListRow) groupDTO {
	dto := toGroupDTO(row.Group)
	dto.AccountCount = row.AccountCount
	dto.PositionCount = row.PositionCount
	return dto
}

// --- balance ----------------------------------------------------------------

// balanceDTO is the wire shape of one per-(account, asset) holdings snapshot.
// All amounts are exact decimal strings passed through verbatim. Available,
// held and incoming are quantities of Asset; realizedPnl and averageEntryPrice
// are denominated in AccountCurrency, which is empty when the account's
// currency cascade sets no tier and the two values therefore carry no unit.
type balanceDTO struct {
	UpdatedAt             time.Time `json:"updatedAt"`
	Account               string    `json:"account"`
	Asset                 string    `json:"asset"`
	Available             string    `json:"available"`
	Held                  string    `json:"held"`
	Incoming              string    `json:"incoming"`
	RealizedPnl           string    `json:"realizedPnl"`
	RealizedPnlHaltReason string    `json:"realizedPnlHaltReason"`
	AverageEntryPrice     string    `json:"averageEntryPrice"`
	AccountCurrency       string    `json:"accountCurrency"`
}

// balanceRealizedPnlRequestDTO is the wire body for updating the realized P&L
// snapshot on one per-(account, asset) balance row.
type balanceRealizedPnlRequestDTO struct {
	Asset       string `json:"asset"`
	RealizedPnl string `json:"realizedPnl"`
}

// toBalanceDTO maps a domain.Balance onto the wire DTO.
func toBalanceDTO(b domain.Balance) balanceDTO {
	return balanceDTO{
		UpdatedAt:             b.UpdatedAt,
		Account:               string(b.Account),
		Asset:                 b.Asset,
		Available:             b.Available,
		Held:                  b.Held,
		Incoming:              b.Incoming,
		RealizedPnl:           b.RealizedPnl,
		RealizedPnlHaltReason: string(b.RealizedPnlHaltReason),
		AverageEntryPrice:     b.AverageEntryPrice,
		AccountCurrency:       b.AccountCurrency,
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
// exact decimal strings passed through verbatim. ID is the optional
// caller-supplied opaque public handle for the adjustment record; when omitted
// the server generates and returns one. It is
// write-only on create: a supplied id is accepted verbatim and must be unique
// (a duplicate is a 409). It is omitted from the request echoed back in an
// adjustment record (the record's own id carries the resolved handle).
type adjustmentRequestDTO struct {
	Balance           *adjustmentAmountDTO `json:"balance,omitempty"`
	BalanceBounds     *adjustmentBoundsDTO `json:"balanceBounds,omitempty"`
	Held              *adjustmentAmountDTO `json:"held,omitempty"`
	HeldBounds        *adjustmentBoundsDTO `json:"heldBounds,omitempty"`
	Incoming          *adjustmentAmountDTO `json:"incoming,omitempty"`
	IncomingBounds    *adjustmentBoundsDTO `json:"incomingBounds,omitempty"`
	ID                string               `json:"id,omitempty"`
	Asset             string               `json:"asset"`
	AverageEntryPrice string               `json:"averageEntryPrice,omitempty"`
	RealizedPnl       string               `json:"realizedPnl,omitempty"`
}

// adjustmentOutcomeDTO is the accept/reject outcome of an adjustment record.
// Exactly one of Accepted/Rejected is non-nil.
type adjustmentOutcomeDTO struct {
	Accepted *adjustmentAcceptedDTO `json:"accepted,omitempty"`
	Rejected *adjustmentRejectedDTO `json:"rejected,omitempty"`
}

// adjustmentAcceptedDTO carries the per-field delta and absolute result.
type adjustmentAcceptedDTO struct {
	BalanceDelta          string                         `json:"balanceDelta"`
	BalanceResult         string                         `json:"balanceResult"`
	HeldDelta             string                         `json:"heldDelta"`
	HeldResult            string                         `json:"heldResult"`
	IncomingDelta         string                         `json:"incomingDelta"`
	IncomingResult        string                         `json:"incomingResult"`
	RealizedPnlResult     adjustmentRealizedPnlResultDTO `json:"realizedPnlResult"`
	RealizedPnlHaltReason string                         `json:"realizedPnlHaltReason,omitempty"`
	AverageEntryPrice     string                         `json:"averageEntryPrice,omitempty"`
}

type adjustmentRealizedPnlResultDTO struct {
	Delta  string `json:"delta"`
	Result string `json:"result"`
}

// adjustmentRejectedDTO carries the structured rejection reason.
type adjustmentRejectedDTO struct {
	Code    string `json:"code"`
	Scope   string `json:"scope,omitempty"`
	Policy  string `json:"policy,omitempty"`
	Reason  string `json:"reason"`
	Details string `json:"details,omitempty"`
}

// adjustmentDTO is the wire shape of one adjustment record incl. outcome. An
// adjustment is a machine record: its public handle is the opaque external id;
// no surrogate id is serialized.
type adjustmentDTO struct {
	At        time.Time            `json:"at"`
	ID        string               `json:"id"`
	Request   adjustmentRequestDTO `json:"request"`
	Outcome   adjustmentOutcomeDTO `json:"outcome"`
	Account   string               `json:"account"`
	Principal string               `json:"principal,omitempty"`
	Source    string               `json:"source"`
	Status    string               `json:"status"`
}

// fromAdjustmentRequestDTO maps a wire request body onto the domain request.
// Decimal values are carried through verbatim; never parsed to float.
func fromAdjustmentRequestDTO(dto adjustmentRequestDTO) domain.AdjustmentRequest {
	return domain.AdjustmentRequest{
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
		RealizedPnl:       req.RealizedPnl,
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
		outcome.Rejected = &adjustmentRejectedDTO{
			Code:    r.Rejected.Code,
			Scope:   r.Rejected.Scope,
			Policy:  r.Rejected.Policy,
			Reason:  r.Rejected.Reason,
			Details: r.Rejected.Details,
		}
	}
	return adjustmentDTO{
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
) adjustmentRealizedPnlResultDTO {
	return adjustmentRealizedPnlResultDTO{
		Delta:  r.Accepted.RealizedPnlDelta,
		Result: r.Accepted.RealizedPnlResult,
	}
}

// --- order ------------------------------------------------------------------

// orderDTO is the wire shape of one Officer-side order record. An order is a
// machine record: its public handle is the opaque external id; no surrogate or
// engine id is serialized. DisplayPrice is the human-readable pre-trade lock
// price derived from the opaque lock blob by the engine seam; the raw lock is
// never serialized. All monetary and size values are
// exact decimal strings passed through verbatim.
type orderDTO struct {
	At                  time.Time       `json:"at"`
	ID                  string          `json:"id"`
	Account             string          `json:"account"`
	Principal           string          `json:"principal,omitempty"`
	BaseAsset           string          `json:"baseAsset"`
	QuoteAsset          string          `json:"quoteAsset"`
	Side                string          `json:"side"`
	AmountKind          string          `json:"amountKind"`
	AmountValue         string          `json:"amountValue"`
	CommissionSubtotals []commissionDTO `json:"commissionSubtotals"`
	// LeavesQuantity is the persisted remaining open base quantity (exact decimal
	// string). It is read from the stored order/report data as-is.
	LeavesQuantity string `json:"leavesQuantity"`
	Price          string `json:"price"`
	Status         string `json:"status"`
	Source         string `json:"source"`
	DisplayPrice   string `json:"displayPrice"`
	DropCopy       bool   `json:"dropCopy"`
	// Signed is the order-level rollup: whether at least one of the order's events
	// carries a persisted Ed25519-signed attestation.
	Signed bool `json:"signed"`
}

type commissionDTO struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// toOrderDTO maps a backend-enriched domain order onto the wire DTO.
// displayPrice comes from the backend's engine seam; this transport mapper
// never decodes the opaque lock. signed reports whether the order carries at
// least one Ed25519-signed event attestation.
func toOrderDTO(o domain.Order, displayPrice string, signed bool) orderDTO {
	return orderDTO{
		At:          o.At,
		ID:          o.ExternalID.String(),
		Account:     string(o.Account),
		Principal:   o.Principal,
		BaseAsset:   o.BaseAsset,
		QuoteAsset:  o.QuoteAsset,
		Side:        string(o.Side),
		AmountKind:  string(o.AmountKind),
		AmountValue: o.AmountValue,
		CommissionSubtotals: toCommissionDTOs(
			o.CommissionSubtotals,
		),
		LeavesQuantity: o.Leaves,
		Price:          o.Price,
		Status:         string(o.Status),
		Source:         string(o.Source),
		DisplayPrice:   displayPrice,
		DropCopy:       o.DropCopy,
		Signed:         signed,
	}
}

// eventAttestationDTO is the wire shape of an event's persisted signed
// attestation envelope. Token is the exact base64url envelope bytes; the rest is
// the envelope metadata. Signed reports whether the envelope carries an Ed25519
// signature (alg "ed25519") versus an eSign-off envelope (alg "none").
type eventAttestationDTO struct {
	Token       string `json:"token"`
	KeyID       string `json:"keyId"`
	Alg         string `json:"alg"`
	RequestType string `json:"requestType"`
	Mode        string `json:"mode"`
	IssuedAt    string `json:"issuedAt"`
	Signed      bool   `json:"signed"`
}

// toEventAttestationDTO maps an event's 1:1 persisted attestation envelope onto
// the wire DTO, or returns nil when the event carries no envelope (unattested).
// The envelope is read back from OrderEvent.Attestation; no surrogate or engine
// id is present in it.
func toEventAttestationDTO(a *domain.EventAttestation) *eventAttestationDTO {
	if a == nil {
		return nil
	}
	return &eventAttestationDTO{
		Token:       a.Token,
		KeyID:       a.KeyID,
		Alg:         a.Alg,
		RequestType: string(a.RequestType),
		Mode:        a.Mode,
		IssuedAt:    a.IssuedAt,
		Signed:      eventAttestationSigned(a),
	}
}

// eventAttestationSigned reports whether a persisted attestation carries a real
// Ed25519 signature, as opposed to an eSign-off ("none") envelope or no envelope
// at all.
func eventAttestationSigned(a *domain.EventAttestation) bool {
	return a != nil && a.Alg == "ed25519"
}

// --- event reproduction -----------------------------------------------------

// eventReproductionESignDTO is the signing-mode facet of a reproduction bundle:
// the current global eSign-off flag plus this event's own envelope alg and
// whether it carries a real signature. It makes explicit whether the event was
// signed ("ed25519") or issued under eSign-off ("none").
type eventReproductionESignDTO struct {
	Alg     string `json:"alg"`
	NoESign bool   `json:"noESign"`
	Signed  bool   `json:"signed"`
}

// eventReproductionRequestDTO is the request bound in the attestation payload,
// reconstructed for reproduction from the decoded token. It surfaces the request
// type and its material params so a verifier sees which request produced the
// recorded result. It carries no private material.
type eventReproductionRequestDTO struct {
	RequestType     string                     `json:"requestType"`
	OrderExternalID string                     `json:"orderId,omitempty"`
	EventExternalID string                     `json:"eventId,omitempty"`
	Instrument      string                     `json:"instrument"`
	Side            string                     `json:"side"`
	Quantity        string                     `json:"quantity"`
	AmountKind      string                     `json:"amountKind"`
	OrderType       string                     `json:"orderType"`
	LimitPrice      string                     `json:"limitPrice"`
	PriceCurrency   string                     `json:"priceCurrency"`
	AccountID       string                     `json:"accountId"`
	Verdict         string                     `json:"verdict"`
	Rejects         []orderRejectDTO           `json:"rejects,omitempty"`
	ExecutionReport *executionReportRequestDTO `json:"executionReport,omitempty"`
	Result          *attestationResultDTO      `json:"result"`
}

// attestationResultDTO is the recorded result section bound in the payload,
// reconstructed for reproduction. It is present for execution reports and
// order confirmation/cancellation shortcuts beyond the submit verdict.
type attestationResultDTO struct {
	Outcome        string                `json:"outcome"`
	FillQuantity   string                `json:"fillQuantity"`
	FillPrice      string                `json:"fillPrice"`
	FillLockPrice  string                `json:"fillLockPrice"`
	Commission     *commissionDTO        `json:"commission"`
	LeavesQuantity string                `json:"leavesQuantity"`
	OrderStatus    string                `json:"orderStatus"`
	Blocks         []attestationBlockDTO `json:"blocks"`
}

// attestationBlockDTO is one engine-recorded account block bound in the payload
// result, reconstructed for reproduction.
type attestationBlockDTO struct {
	Account string `json:"account"`
	Policy  string `json:"policy"`
	Code    string `json:"code"`
	Reason  string `json:"reason"`
	Details string `json:"details"`
}

// eventReproductionDTO is the controller-facing reproduction bundle for a single
// order-history event's attestation: byte-for-byte what a robot / AI agent
// received from the live APIs for the request that produced this event, assembled
// from persisted state through the SAME serializers the live endpoints use so it
// can never drift from real API output.
//
// A field is null when the event carries no persisted attestation envelope:
// attestation, request, response, canonicalApproval and publicKey are then all
// null and reason explains why. publicKey and signature are also null/empty
// under eSign-off ("none"), where there is no signing key or signature to
// reproduce.
type eventReproductionDTO struct {
	// RequestType is the trading request the attestation binds ("submit" |
	// "execution_report" | "confirm" | "cancel"); empty when unattested.
	RequestType string `json:"requestType"`
	// Event is byte-identical to the GET /orders/{id} event body (same serializer).
	Event orderEventDTO `json:"event"`
	// Attestation is the persisted envelope metadata (token verbatim), or null
	// when the event has no attestation.
	Attestation *eventAttestationDTO `json:"attestation"`
	// Request is the request bound in the attestation payload, reconstructed from
	// the decoded token; null when the event has no attestation or the token
	// cannot be decoded.
	Request *eventReproductionRequestDTO `json:"request"`
	// Response is the exact type-specific API response the robot received for this
	// request, reconstructed from persisted state through the SAME serializer
	// (token verbatim); null when the event has no attestation.
	Response *eventReproductionResponseDTO `json:"response"`
	// CanonicalApproval is the exact signed bytes as a string: the attestation
	// payload in its canonical signed form, byte-identical to what was signed.
	// Null when the event has no attestation or the token cannot be decoded.
	CanonicalApproval *string `json:"canonicalApproval"`
	// PublicKey is the public key matching this attestation's keyId, resolved
	// rotation-safe by id. Null under alg "none" / no signature.
	PublicKey *publicKeyMaterialDTO `json:"publicKey"`
	// ESign reports the signing mode and whether this event was signed.
	ESign eventReproductionESignDTO `json:"eSign"`
	// Signature is the base64 envelope signature; empty under alg "none".
	Signature string `json:"signature"`
	// Reason explains a null attestation (e.g. the request was not attested);
	// empty when an attestation is present.
	Reason string `json:"reason"`
}

// eventReproductionResponseDTO is the exact type-specific API response the robot
// received for the attested request, reconstructed through the same serializers.
// Exactly one facet is populated, matching the request type. The token facet
// carries the attestation token verbatim.
type eventReproductionResponseDTO struct {
	// SubmitResponse reproduces the POST /orders/submit response (submit).
	SubmitResponse *approvalTokenDTO `json:"submitResponse,omitempty"`
	// ExecutionReport reproduces the POST .../execution-reports response.
	ExecutionReport *executionReportResponseDTO `json:"executionReport,omitempty"`
	// Confirm reproduces the POST .../confirm response.
	Confirm *orderMutationResponseDTO `json:"confirm,omitempty"`
	// Cancel reproduces the POST .../cancel response.
	Cancel *orderMutationResponseDTO `json:"cancel,omitempty"`
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
// would-be display (lock) price on pass, or the structured rejects and any
// would-be account block on reject. The display price is an exact decimal
// string, never the opaque lock blob.
type checkResultDTO struct {
	WouldBlock        *executionBlockDTO `json:"wouldBlock"`
	Rejects           []orderRejectDTO   `json:"rejects"`
	WouldDisplayPrice string             `json:"wouldDisplayPrice"`
	Passed            bool               `json:"passed"`
}

func toOrderRejectDTOs(rejects []domain.OrderReject) []orderRejectDTO {
	if len(rejects) == 0 {
		return nil
	}
	out := make([]orderRejectDTO, 0, len(rejects))
	for _, rej := range rejects {
		out = append(out, orderRejectDTO{
			Code:    rej.Code,
			Scope:   rej.Scope,
			Policy:  rej.Policy,
			Reason:  rej.Reason,
			Details: rej.Details,
		})
	}
	return out
}

// toCheckResultDTO maps a domain.CheckResult onto the wire DTO. It reuses the
// execution-block shape for the would-be block.
func toCheckResultDTO(r domain.CheckResult) checkResultDTO {
	rejects := toOrderRejectDTOs(r.Rejects)
	if rejects == nil {
		rejects = []orderRejectDTO{}
	}
	var block *executionBlockDTO
	if r.WouldBlock != nil {
		block = &executionBlockDTO{
			Account: string(r.WouldBlock.Account),
			Policy:  r.WouldBlock.Policy,
			Code:    r.WouldBlock.Code,
			Reason:  r.WouldBlock.Reason,
			Details: r.WouldBlock.Details,
		}
	}
	return checkResultDTO{
		WouldBlock:        block,
		Rejects:           rejects,
		WouldDisplayPrice: r.WouldLockPrice,
		Passed:            r.Passed,
	}
}

// --- order event ------------------------------------------------------------

// orderEventDTO is the wire shape of one immutable order lifecycle event. The
// payload fields are flattened in; only the ones relevant to the type are set.
// Reject fields can also be present on fill events that caused an account block.
// Signed reports whether the event carries an Ed25519-signed attestation, and
// Alg is that attestation's algorithm ("ed25519" | "none"); Alg is empty when
// the event is unattested, so the web can render a per-event key icon and open
// the per-event reproduction.
type orderEventDTO struct {
	At              time.Time                  `json:"at"`
	ID              string                     `json:"id"`
	Order           string                     `json:"order"`
	Type            string                     `json:"type"`
	Source          string                     `json:"source"`
	Alg             string                     `json:"alg,omitempty"`
	Principal       string                     `json:"principal,omitempty"`
	RejectCode      string                     `json:"rejectCode,omitempty"`
	RejectScope     string                     `json:"rejectScope,omitempty"`
	RejectPolicy    string                     `json:"rejectPolicy,omitempty"`
	RejectReason    string                     `json:"rejectReason,omitempty"`
	RejectDetails   string                     `json:"rejectDetails,omitempty"`
	Rejects         []orderRejectDTO           `json:"rejects,omitempty"`
	FillQuantity    string                     `json:"fillQuantity,omitempty"`
	FillPrice       string                     `json:"fillPrice,omitempty"`
	FillLockPrice   string                     `json:"fillLockPrice,omitempty"`
	LeavesQuantity  string                     `json:"leavesQuantity,omitempty"`
	OrderStatus     string                     `json:"orderStatus,omitempty"`
	ExecutionReport *executionReportRequestDTO `json:"executionReport,omitempty"`
	Commission      *commissionDTO             `json:"commission,omitempty"`
	Signed          bool                       `json:"signed"`
}

// executionReportRequestDTO is the audit-safe execution-report input captured
// by the event before node enrichment. No public field is omitted so
// empty/false/null values remain distinguishable. The opaque engine lock is
// never exposed.
type executionReportRequestDTO struct {
	ID             string         `json:"id"`
	BaseAsset      string         `json:"baseAsset"`
	QuoteAsset     string         `json:"quoteAsset"`
	FillQuantity   string         `json:"fillQuantity"`
	FillPrice      string         `json:"fillPrice"`
	LeavesQuantity string         `json:"leavesQuantity"`
	LockPrice      string         `json:"lockPrice"`
	Commission     *commissionDTO `json:"commission"`
	Order          string         `json:"order"`
	Account        string         `json:"account"`
	Side           string         `json:"side"`
	OrderStatus    string         `json:"orderStatus"`
	Force          bool           `json:"force"`
}

// toOrderEventDTO maps a domain.OrderEvent onto the wire DTO. Both the event and
// its parent order are addressed by opaque external ids; no surrogate id appears.
// The event's 1:1 attestation, when present, sets the signed flag and alg.
func toOrderEventDTO(e domain.OrderEvent) orderEventDTO {
	dto := orderEventDTO{
		At:              e.At,
		ID:              e.ExternalID.String(),
		Order:           e.Order.String(),
		Type:            string(e.Type),
		Source:          string(e.Source),
		Principal:       e.Principal,
		RejectCode:      e.Payload.RejectCode,
		RejectScope:     e.Payload.RejectScope,
		RejectPolicy:    e.Payload.RejectPolicy,
		RejectReason:    e.Payload.RejectReason,
		RejectDetails:   e.Payload.RejectDetails,
		Rejects:         toOrderRejectDTOs(e.Payload.Rejects),
		FillQuantity:    e.Payload.FillQuantity,
		FillPrice:       e.Payload.FillPrice,
		FillLockPrice:   e.Payload.FillLockPrice,
		LeavesQuantity:  e.Payload.LeavesQuantity,
		OrderStatus:     e.Payload.OrderStatus,
		ExecutionReport: toExecutionReportRequestDTO(e.Payload.ExecutionReport),
		Commission:      toCommissionDTO(e.Payload.Commission),
	}
	if e.Attestation != nil {
		dto.Alg = e.Attestation.Alg
		dto.Signed = eventAttestationSigned(e.Attestation)
	}
	return dto
}

func toExecutionReportRequestDTO(
	in *domain.ExecutionReportRequest,
) *executionReportRequestDTO {
	if in == nil {
		return nil
	}
	return &executionReportRequestDTO{
		ID:             in.ExternalID.String(),
		BaseAsset:      in.BaseAsset,
		QuoteAsset:     in.QuoteAsset,
		FillQuantity:   in.FillQuantity,
		FillPrice:      in.FillPrice,
		LeavesQuantity: in.LeavesQuantity,
		LockPrice:      in.LockPrice,
		Commission:     toCommissionDTO(in.Commission),
		Order:          in.Order.String(),
		Account:        string(in.Account),
		Side:           string(in.Side),
		OrderStatus:    string(in.OrderStatus),
		Force:          in.Force,
	}
}

// --- trade ------------------------------------------------------------------

// tradeDTO is the wire shape of one per-fill trade record. All monetary values
// are exact decimal strings passed through verbatim.
type tradeDTO struct {
	At         time.Time      `json:"at"`
	ExternalID string         `json:"id"`
	Order      string         `json:"order"`
	Account    string         `json:"account"`
	Principal  string         `json:"principal,omitempty"`
	BaseAsset  string         `json:"baseAsset"`
	QuoteAsset string         `json:"quoteAsset"`
	Side       string         `json:"side"`
	Quantity   string         `json:"quantity"`
	Price      string         `json:"price"`
	LockPrice  string         `json:"lockPrice"`
	Commission *commissionDTO `json:"commission,omitempty"`
	Source     string         `json:"source"`
}

// toTradeDTO maps a domain.Trade onto the wire DTO. Both the trade and its
// originating order are addressed by opaque external ids; no surrogate id appears.
func toTradeDTO(t domain.Trade) tradeDTO {
	return tradeDTO{
		At:         t.At,
		ExternalID: t.ExternalID.String(),
		Order:      t.Order.String(),
		Account:    string(t.Account),
		Principal:  t.Principal,
		BaseAsset:  t.BaseAsset,
		QuoteAsset: t.QuoteAsset,
		Side:       string(t.Side),
		Quantity:   t.Quantity,
		Price:      t.Price,
		LockPrice:  t.LockPrice,
		Commission: toCommissionDTO(t.Commission),
		Source:     string(t.Source),
	}
}

func toCommissionDTO(c *domain.Commission) *commissionDTO {
	if c == nil {
		return nil
	}
	return &commissionDTO{Amount: c.Amount, Currency: c.Currency}
}

func toCommissionDTOs(in []domain.Commission) []commissionDTO {
	if in == nil {
		return []commissionDTO{}
	}
	out := make([]commissionDTO, 0, len(in))
	for _, c := range in {
		out = append(out, *toCommissionDTO(&c))
	}
	return out
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

// executionBlockDTO is one engine-recorded account block from a report.
type executionBlockDTO struct {
	Account string `json:"account"`
	Policy  string `json:"policy"`
	Code    string `json:"code"`
	Reason  string `json:"reason"`
	Details string `json:"details"`
}

// executionReportResponseDTO is the response of POST
// /orders/{id}/execution-reports: the recorded result plus the
// attestation token proving what Officer recorded. AttestationToken is present
// on success; signing or attestation persistence failures fail the request
// before the report is committed.
type executionReportResponseDTO struct {
	ID               string             `json:"id"`
	Result           executionResultDTO `json:"result"`
	AttestationToken string             `json:"attestationToken,omitempty"`
	AttestationKeyID string             `json:"attestationKeyId,omitempty"`
	Signed           bool               `json:"signed"`
}

// orderMutationResponseDTO is the response of POST /orders/{id}/confirm
// and .../cancel: the updated order plus the attestation token proving which
// workflow shortcut Officer recorded. AttestationToken is present on success;
// signing or attestation persistence failures fail the request.
type orderMutationResponseDTO struct {
	Order            orderDTO `json:"order"`
	AttestationToken string   `json:"attestationToken,omitempty"`
	AttestationKeyID string   `json:"attestationKeyId,omitempty"`
	Signed           bool     `json:"signed"`
}

// toExecutionResultDTO maps an engine.ExecutionReportResult onto the wire DTO.
func toExecutionResultDTO(r engine.ExecutionReportResult) executionResultDTO {
	blocks := make([]executionBlockDTO, 0, len(r.Blocks))
	for _, b := range r.Blocks {
		blocks = append(blocks, executionBlockDTO{
			Account: string(b.Account),
			Policy:  b.Policy,
			Code:    b.Code,
			Reason:  b.Reason,
			Details: b.Details,
		})
	}
	outcomes := make([]executionOutcomeDTO, 0, len(r.Outcomes))
	for _, o := range r.Outcomes {
		outcomes = append(outcomes, executionOutcomeDTO{
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

type signingConfigUpdateDTO struct {
	NoESign *bool `json:"noESign"`
}

// publicKeyDTO is the body of GET /signing/keys/active/public.
type publicKeyDTO struct {
	PublicKey string `json:"publicKey"`
}

// publicKeyMaterialDTO is the identified public-key shape used by the
// reproduction bundle and the per-key public endpoint. It carries the public key
// paired with the id it was resolved by and its export format, so a caller can
// resolve a token's embedded keyId to the exact public material. Private
// material is NEVER present.
type publicKeyMaterialDTO struct {
	KeyID  string `json:"keyId"`
	Alg    string `json:"alg"`
	Format string `json:"format"`
	Key    string `json:"key"`
}

// submitOrderTokenRequestDTO is the body of POST /orders/submit. Submit creates
// the order from these fields: there is no prior persisting create. ExternalID
// is the optional caller-supplied opaque public handle; when omitted the server
// generates and returns one in the response. A supplied id is accepted verbatim
// and must be unique (a duplicate is a 409); it is never a surrogate or engine
// id.
type submitOrderTokenRequestDTO struct {
	ID          string `json:"id,omitempty"`
	Account     string `json:"account"`
	BaseAsset   string `json:"baseAsset"`
	QuoteAsset  string `json:"quoteAsset"`
	Side        string `json:"side"`
	AmountKind  string `json:"amountKind"`
	AmountValue string `json:"amountValue"`
	Price       string `json:"price"`
	Mode        string `json:"mode"`
}

func (d submitOrderTokenRequestDTO) orderFields() submitOrderFieldsDTO {
	return submitOrderFieldsDTO{
		ID: d.ID, Account: d.Account, BaseAsset: d.BaseAsset,
		QuoteAsset: d.QuoteAsset, Side: d.Side, AmountKind: d.AmountKind,
		AmountValue: d.AmountValue, Price: d.Price,
	}
}

type submitDropCopyOrderRequestDTO struct {
	ID          string `json:"id"`
	Account     string `json:"account"`
	BaseAsset   string `json:"baseAsset"`
	QuoteAsset  string `json:"quoteAsset"`
	Side        string `json:"side"`
	AmountKind  string `json:"amountKind"`
	AmountValue string `json:"amountValue"`
	Price       string `json:"price"`
}

func (d submitDropCopyOrderRequestDTO) orderFields() submitOrderFieldsDTO {
	return submitOrderFieldsDTO(d)
}

type submitOrderFieldsDTO struct {
	ID          string
	Account     string
	BaseAsset   string
	QuoteAsset  string
	Side        string
	AmountKind  string
	AmountValue string
	Price       string
}

// approvalTokenDTO is the response of POST /orders/submit. The authorised order
// is referenced by its opaque external id, never a surrogate id.
type approvalTokenDTO struct {
	Token           string           `json:"token"`
	KeyID           string           `json:"keyId"`
	OrderExternalID string           `json:"id"`
	Verdict         string           `json:"verdict"`
	Reasons         []orderRejectDTO `json:"reasons,omitempty"`
}

type dropCopyOrderResponseDTO struct {
	OrderExternalID string `json:"id"`
	Status          string `json:"status"`
}

// confirmExecutionRequestDTO is the body of POST /orders/{id}/confirm.
type confirmExecutionRequestDTO struct {
	Token string `json:"token"`
}

// cancelOrderRequestDTO is the body of POST /orders/{id}/cancel.
type cancelOrderRequestDTO struct {
	Token          string `json:"token"`
	LeavesQuantity string `json:"leavesQuantity"`
	Reason         string `json:"reason"`
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
	OrdersActive   int `json:"ordersActive"`
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
			OrdersActive:   o.Counts.OrdersActive,
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
