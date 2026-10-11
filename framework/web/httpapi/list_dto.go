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

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

// The DTOs below are the JSON wire shapes of account, group, order and trade
// rows. The REST surface and the MCP tools share them so both return the same
// row.

// AccountDTO is the wire shape of a single account. An account is a dictionary
// record: its public handle is the code, paired with a mutable title.
// The engine account id and the store surrogate id are never serialized.
//
// The blocked/blockReason/blockSource triple is the EFFECTIVE kill-switch
// state: the account's own block joined with its group's, so a consumer that
// reads only blocked gets the answer to "can this account trade right now".
// The per-tier fields carry the two independent underlying blocks.
type AccountDTO struct {
	Code              string             `json:"code"`
	Title             string             `json:"title"`
	Pnl               string             `json:"pnl"`
	PnlHaltReason     string             `json:"pnlHaltReason"`
	Group             string             `json:"group"`
	Currency          string             `json:"currency"`
	EffectiveCurrency string             `json:"effectiveCurrency"`
	CurrencyOrigin    string             `json:"currencyOrigin"`
	CurrencyCascade   CurrencyCascadeDTO `json:"currencyCascade"`
	Notes             string             `json:"notes"`
	PositionCount     int                `json:"positionCount"`
	// VenueAccess is set on list rows only (ToAccountRowDTO), nil on a
	// single-account read: a grant on at least one trading connection, the
	// account's own or its group's.
	VenueAccess        *bool  `json:"venueAccess,omitempty"`
	BlockReason        string `json:"blockReason"`
	BlockSource        string `json:"blockSource"`
	AccountBlockReason string `json:"accountBlockReason"`
	GroupBlockReason   string `json:"groupBlockReason"`
	Blocked            bool   `json:"blocked"`
	AccountBlocked     bool   `json:"accountBlocked"`
	GroupBlocked       bool   `json:"groupBlocked"`
}

// CurrencyCascadeDTO is the wire shape of an account's currency tiers: its own,
// its group's and the default.
type CurrencyCascadeDTO struct {
	Account string `json:"account"`
	Group   string `json:"group"`
	Default string `json:"default"`
}

// ToAccountDTO maps a domain.Account onto the wire DTO. The account's public
// handle is its code; the engine account id is never serialized. block is the
// account joined with its group, resolved by the caller, which owns the group
// read; the account row alone carries only the account's own latched flag.
func ToAccountDTO(a domain.Account, block domain.AccountBlockState) AccountDTO {
	return AccountDTO{
		Code:              string(a.Code),
		Title:             a.Title,
		Pnl:               a.Pnl,
		PnlHaltReason:     string(a.PnlHaltReason),
		Group:             a.GroupCode,
		Currency:          a.Currency,
		EffectiveCurrency: a.EffectiveCurrency,
		CurrencyOrigin:    a.CurrencyOrigin,
		CurrencyCascade: CurrencyCascadeDTO{
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

// ToAccountRowDTO maps a list row onto the account wire DTO.
func ToAccountRowDTO(
	row store.AccountListRow, block domain.AccountBlockState,
) AccountDTO {
	dto := ToAccountDTO(row.Account, block)
	dto.PositionCount = row.PositionCount
	dto.VenueAccess = &row.VenueAccess
	return dto
}

// GroupDTO is the wire shape of a single account group. A group is a dictionary
// record: its public handle is the code, paired with a mutable title.
// The engine group id and the store surrogate id are never serialized.
type GroupDTO struct {
	Code          string `json:"code"`
	Title         string `json:"title"`
	Currency      string `json:"currency"`
	Notes         string `json:"notes"`
	AccountCount  int    `json:"accountCount"`
	PositionCount int    `json:"positionCount"`
	// VenueAccess is set on list rows only (ToGroupRowDTO), nil on a
	// single-group read: a grant on at least one trading connection to the
	// group itself.
	VenueAccess *bool  `json:"venueAccess,omitempty"`
	BlockReason string `json:"blockReason"`
	Blocked     bool   `json:"blocked"`
}

// ToGroupDTO maps a domain.AccountGroup onto the wire DTO. The group's public
// handle is its code; the engine group id is never serialized.
func ToGroupDTO(g domain.AccountGroup) GroupDTO {
	return GroupDTO{
		Code:        g.Code,
		Title:       g.Title,
		Currency:    g.Currency,
		Notes:       g.Notes,
		BlockReason: g.BlockReason,
		Blocked:     g.Blocked,
	}
}

// ToGroupRowDTO maps a list row onto the group wire DTO.
func ToGroupRowDTO(row store.GroupListRow) GroupDTO {
	dto := ToGroupDTO(row.Group)
	dto.AccountCount = row.AccountCount
	dto.PositionCount = row.PositionCount
	dto.VenueAccess = &row.VenueAccess
	return dto
}

// OrderDTO is the wire shape of one Officer-side order record. An order is a
// machine record: its public handle is the opaque external id; no surrogate or
// engine id is serialized. DisplayPrice is the human-readable pre-trade lock
// price derived from the opaque lock blob by the engine seam; the raw lock is
// never serialized. All monetary and size values are
// exact decimal strings passed through verbatim.
type OrderDTO struct {
	At                  time.Time       `json:"at"`
	ID                  string          `json:"id"`
	Account             string          `json:"account"`
	Principal           string          `json:"principal,omitempty"`
	BaseAsset           string          `json:"baseAsset"`
	QuoteAsset          string          `json:"quoteAsset"`
	Side                string          `json:"side"`
	AmountKind          string          `json:"amountKind"`
	AmountValue         string          `json:"amountValue"`
	CommissionSubtotals []CommissionDTO `json:"commissionSubtotals"`
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
	Signed          bool   `json:"signed"`
	VenueConnection string `json:"venueConnection,omitempty"`
}

// CommissionDTO is the wire shape of one commission amount and its currency.
type CommissionDTO struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// ToOrderDTO maps a backend-enriched domain order onto the wire DTO.
// displayPrice comes from the backend's engine seam; this transport mapper
// never decodes the opaque lock. signed reports whether the order carries at
// least one Ed25519-signed event attestation.
func ToOrderDTO(o domain.Order, displayPrice string, signed bool) OrderDTO {
	return OrderDTO{
		At:          o.At,
		ID:          o.ExternalID.String(),
		Account:     string(o.Account),
		Principal:   o.Principal,
		BaseAsset:   o.BaseAsset,
		QuoteAsset:  o.QuoteAsset,
		Side:        string(o.Side),
		AmountKind:  string(o.AmountKind),
		AmountValue: o.AmountValue,
		CommissionSubtotals: ToCommissionDTOs(
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

// ToOrderRowDTO maps a list row onto the order wire DTO.
func ToOrderRowDTO(row backend.OrderListRow) OrderDTO {
	dto := ToOrderDTO(row.Order, row.DisplayPrice, row.Signed)
	dto.VenueConnection = row.VenueConnection.String()
	return dto
}

// TradeDTO is the wire shape of one per-fill trade record. All monetary values
// are exact decimal strings passed through verbatim.
type TradeDTO struct {
	At              time.Time      `json:"at"`
	ExternalID      string         `json:"id"`
	Order           string         `json:"order"`
	Account         string         `json:"account"`
	Principal       string         `json:"principal,omitempty"`
	BaseAsset       string         `json:"baseAsset"`
	QuoteAsset      string         `json:"quoteAsset"`
	Side            string         `json:"side"`
	Quantity        string         `json:"quantity"`
	Price           string         `json:"price"`
	LockPrice       string         `json:"lockPrice"`
	Commission      *CommissionDTO `json:"commission,omitempty"`
	Source          string         `json:"source"`
	VenueConnection string         `json:"venueConnection,omitempty"`
}

// ToTradeDTO maps a domain.Trade onto the wire DTO. Both the trade and its
// originating order are addressed by opaque external ids; no surrogate id appears.
func ToTradeDTO(t domain.Trade) TradeDTO {
	return TradeDTO{
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
		Commission: ToCommissionDTO(t.Commission),
		Source:     string(t.Source),
	}
}

// ToTradeRowDTO maps a list row onto the trade wire DTO.
func ToTradeRowDTO(row store.TradeListRow) TradeDTO {
	dto := ToTradeDTO(row.Trade)
	dto.VenueConnection = row.VenueConnection.String()
	return dto
}

// ToCommissionDTO maps one commission onto its wire DTO; nil stays nil.
func ToCommissionDTO(c *domain.Commission) *CommissionDTO {
	if c == nil {
		return nil
	}
	return &CommissionDTO{Amount: c.Amount, Currency: c.Currency}
}

// ToCommissionDTOs maps commissions onto wire DTOs; nil maps to an empty slice,
// so the JSON is [] and never null.
func ToCommissionDTOs(in []domain.Commission) []CommissionDTO {
	if in == nil {
		return []CommissionDTO{}
	}
	out := make([]CommissionDTO, 0, len(in))
	for _, c := range in {
		out = append(out, *ToCommissionDTO(&c))
	}
	return out
}
