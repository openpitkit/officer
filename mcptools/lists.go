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
	"maps"
	"slices"
	"strings"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	"go.openpit.dev/officer/framework/store"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

// The list tools map their JSON arguments 1:1 onto the core list filters and
// leave every value rule to the filter's Validate. The only checks made here
// are the decoding the JSON encoding needs (RFC3339Nano times, external ids)
// and the tools' own paging arguments.

const (
	listOrdersToolName   = "list_orders"
	listTradesToolName   = "list_trades"
	listAccountsToolName = "list_accounts"
	listGroupsToolName   = "list_groups"
)

var (
	listOrdersToolDescription = listToolDescription(
		"Return one page of orders; each row is the order row of the REST order list.",
		"account (exact account code), source, side (buy | sell), status (order "+
			"statuses), baseAsset and quoteAsset (text match), amount and price "+
			"(decimal range), at (time range)",
		store.OrderSortKeys(),
	)
	listTradesToolDescription = listToolDescription(
		"Return one page of trades; each row is the trade row of the REST trade list.",
		"account, baseAsset and quoteAsset (text match), id (exact trade id), side "+
			"(buy | sell), source, at (time range), quantity, price and lockPrice "+
			"(decimal range)",
		store.TradeSortKeys(),
	)
	listAccountsToolDescription = listToolDescription(
		"Return one page of accounts; each row is the account row of the REST "+
			"account list, its block joined with its group's.",
		"group (exact group code; an empty string selects accounts in no group), "+
			"code (code or title) and blockReason (text match), status (active | "+
			"blocked), position (open-position count range), pnl (P&L bounds in "+
			"pnl.currency) with includeUnsetPnl and includeHaltedPnl",
		store.AccountSortKeys(),
	)
	listGroupsToolDescription = listToolDescription(
		"Return one page of account groups; each row is the group row of the REST "+
			"group list. The first page also carries the default group when it "+
			"matches the filters, outside the limit and the total.",
		"code (code or title), notes and blockReason (text match), status (active "+
			"| blocked), position (open-position count range), account "+
			"(member-account count range)",
		store.GroupSortKeys(),
	)
)

// listToolDescription composes a list tool description from the tool's own
// lead and filters and the parts the four list tools share. The sort columns
// come from the core key set, so the text cannot drift from what Validate
// accepts.
func listToolDescription(lead, filters string, sortKeys map[string]struct{}) string {
	return fmt.Sprintf(
		"%s Read-only - no secrets, no order-flow control. Filters: %s; venue is "+
			"the trading-connection filter: mode any (through at least one trading "+
			"connection), none (through no connection) or connection (through the "+
			"connection whose id is in connection). Sort columns: %s. Paging: limit "+
			"rows per page (default %d, capped at %d) and a 1-based page; total "+
			"counts the matching rows before paging. An invalid value fails with a "+
			"validation error.",
		lead, filters, strings.Join(slices.Sorted(maps.Keys(sortKeys)), ", "),
		listDefaultLimit, listMaxLimit,
	)
}

// textMatchInput is the JSON form of store.TextMatcher.
type textMatchInput struct {
	Fragments   []string `json:"fragments,omitempty" jsonschema:"Literal fragments that must appear in this order"`
	AnchorStart bool     `json:"anchorStart,omitempty" jsonschema:"The first fragment matches from the start of the value"`
	AnchorEnd   bool     `json:"anchorEnd,omitempty" jsonschema:"The last fragment ends at the end of the value"`
}

// matcher returns the core matcher; an omitted match is the unrestricted zero
// matcher.
func (m *textMatchInput) matcher() store.TextMatcher {
	if m == nil {
		return store.TextMatcher{}
	}
	return store.TextMatcher{
		Fragments:   m.Fragments,
		AnchorStart: m.AnchorStart,
		AnchorEnd:   m.AnchorEnd,
	}
}

// countRangeInput is the JSON form of store.CountRangeFilter.
type countRangeInput struct {
	Min          *int `json:"min,omitempty" jsonschema:"Lower bound"`
	Max          *int `json:"max,omitempty" jsonschema:"Upper bound"`
	Equal        *int `json:"equal,omitempty" jsonschema:"Exact count"`
	NotEqual     *int `json:"notEqual,omitempty" jsonschema:"Count to exclude"`
	MinExclusive bool `json:"minExclusive,omitempty" jsonschema:"Exclude the lower bound itself"`
	MaxExclusive bool `json:"maxExclusive,omitempty" jsonschema:"Exclude the upper bound itself"`
}

// filter returns the core count range; an omitted range is unrestricted.
func (r *countRangeInput) filter() store.CountRangeFilter {
	if r == nil {
		return store.CountRangeFilter{}
	}
	return store.CountRangeFilter{
		Min:          r.Min,
		Max:          r.Max,
		Equal:        r.Equal,
		NotEqual:     r.NotEqual,
		MinExclusive: r.MinExclusive,
		MaxExclusive: r.MaxExclusive,
	}
}

// decimalRangeInput is the JSON form of store.DecimalRangeFilter.
type decimalRangeInput struct {
	Min          *string `json:"min,omitempty" jsonschema:"Lower bound, an exact decimal string"`
	Max          *string `json:"max,omitempty" jsonschema:"Upper bound, an exact decimal string"`
	Equal        *string `json:"equal,omitempty" jsonschema:"Exact value, an exact decimal string"`
	NotEqual     *string `json:"notEqual,omitempty" jsonschema:"Value to exclude, an exact decimal string"`
	MinExclusive bool    `json:"minExclusive,omitempty" jsonschema:"Exclude the lower bound itself"`
	MaxExclusive bool    `json:"maxExclusive,omitempty" jsonschema:"Exclude the upper bound itself"`
}

// filter returns the core decimal range; an omitted range is unrestricted.
func (r *decimalRangeInput) filter() store.DecimalRangeFilter {
	if r == nil {
		return store.DecimalRangeFilter{}
	}
	return store.DecimalRangeFilter{
		Min:          r.Min,
		Max:          r.Max,
		Equal:        r.Equal,
		NotEqual:     r.NotEqual,
		MinExclusive: r.MinExclusive,
		MaxExclusive: r.MaxExclusive,
	}
}

// timeRangeInput is the JSON form of store.TimeRangeFilter.
type timeRangeInput struct {
	Min          *string `json:"min,omitempty" jsonschema:"Earliest time, RFC3339Nano"`
	Max          *string `json:"max,omitempty" jsonschema:"Latest time, RFC3339Nano"`
	MinExclusive bool    `json:"minExclusive,omitempty" jsonschema:"Exclude the earliest time itself"`
	MaxExclusive bool    `json:"maxExclusive,omitempty" jsonschema:"Exclude the latest time itself"`
}

// filter returns the core time range, its bounds parsed as RFC3339Nano and
// held in UTC; field names the argument in a parse error. An omitted range is
// unrestricted.
func (r *timeRangeInput) filter(field string) (store.TimeRangeFilter, error) {
	if r == nil {
		return store.TimeRangeFilter{}, nil
	}
	minTime, err := parseListTime(field+".min", r.Min)
	if err != nil {
		return store.TimeRangeFilter{}, err
	}
	maxTime, err := parseListTime(field+".max", r.Max)
	if err != nil {
		return store.TimeRangeFilter{}, err
	}
	return store.TimeRangeFilter{
		Min:          minTime,
		Max:          maxTime,
		MinExclusive: r.MinExclusive,
		MaxExclusive: r.MaxExclusive,
	}, nil
}

// parseListTime parses an optional RFC3339Nano bound into UTC; an omitted
// bound stays nil.
func parseListTime(field string, raw *string) (*time.Time, error) {
	if raw == nil {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, *raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %q is not an RFC3339Nano time: %w", field, *raw, domain.ErrInvalid)
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

// venueInput is the JSON form of store.VenueFilter.
type venueInput struct {
	Mode       string `json:"mode,omitempty" jsonschema:"Trading-connection filter mode: any = through at least one trading connection; none = through no connection; connection = through the connection named in connection; omitted = unrestricted"`
	Connection string `json:"connection,omitempty" jsonschema:"Trading connection id, for mode connection"`
}

// filter returns the core venue filter. The mode passes as given and the mode
// and id rules are VenueFilter.Validate's; an omitted filter is unrestricted.
func (v *venueInput) filter() (store.VenueFilter, error) {
	if v == nil {
		return store.VenueFilter{}, nil
	}
	f := store.VenueFilter{Mode: store.VenueFilterMode(v.Mode)}
	if v.Connection != "" {
		id, err := domain.ParseExternalID(v.Connection)
		if err != nil {
			return store.VenueFilter{}, fmt.Errorf("venue.connection: %w", err)
		}
		f.ConnectionID = id
	}
	return f, nil
}

// sortInput is the JSON form of store.SortSpec.
type sortInput struct {
	Column     string `json:"column,omitempty" jsonschema:"Sort column, one the tool description lists; omitted keeps the natural order"`
	Descending bool   `json:"descending,omitempty" jsonschema:"Sort in descending order"`
}

// spec returns the core sort; an omitted sort keeps the natural order.
func (s *sortInput) spec() store.SortSpec {
	if s == nil {
		return store.SortSpec{}
	}
	return store.SortSpec{Column: s.Column, Descending: s.Descending}
}

// listPage turns the tools' paging arguments into the core page window: limit
// 0 is the default and a limit above the cap is the cap, page 0 is the first
// page, and a negative value is invalid; store.PageAt decides the offset and
// rejects one past int.
func listPage(limit, page int) (store.PageSpec, error) {
	if limit < 0 {
		return store.PageSpec{}, fmt.Errorf("limit: must not be negative: %w", domain.ErrInvalid)
	}
	if page < 0 {
		return store.PageSpec{}, fmt.Errorf("page: must not be negative: %w", domain.ErrInvalid)
	}
	if limit == 0 {
		limit = listDefaultLimit
	}
	if limit > listMaxLimit {
		limit = listMaxLimit
	}
	if page == 0 {
		page = 1
	}
	return store.PageAt(limit, page)
}

// listSide returns the core side filter; an omitted side is unrestricted.
func listSide(side string) *domain.OrderSide {
	if side == "" {
		return nil
	}
	s := domain.OrderSide(side)
	return &s
}

type listOrdersInput struct {
	Venue      *venueInput        `json:"venue,omitempty" jsonschema:"Trading-connection filter on the order's venue link"`
	Account    string             `json:"account,omitempty" jsonschema:"Exact account code"`
	Source     string             `json:"source,omitempty" jsonschema:"Exact source channel: panel | api | mcp | system"`
	Side       string             `json:"side,omitempty" jsonschema:"Order side: buy | sell"`
	Status     []string           `json:"status,omitempty" jsonschema:"Order statuses to keep"`
	BaseAsset  *textMatchInput    `json:"baseAsset,omitempty" jsonschema:"Base asset code match"`
	QuoteAsset *textMatchInput    `json:"quoteAsset,omitempty" jsonschema:"Quote asset code match"`
	Amount     *decimalRangeInput `json:"amount,omitempty" jsonschema:"Order amount range"`
	Price      *decimalRangeInput `json:"price,omitempty" jsonschema:"Order price range"`
	At         *timeRangeInput    `json:"at,omitempty" jsonschema:"Order time range"`
	Sort       *sortInput         `json:"sort,omitempty" jsonschema:"Sort order"`
	Limit      int                `json:"limit,omitempty" jsonschema:"Rows per page, default 50, capped at 500"`
	Page       int                `json:"page,omitempty" jsonschema:"1-based page number, default 1"`
}

type listOrdersOutput struct {
	Orders []httpx.OrderDTO `json:"orders"`
	Total  int              `json:"total"`
}

func (in listOrdersInput) filter() (store.OrderListFilter, error) {
	venue, err := in.Venue.filter()
	if err != nil {
		return store.OrderListFilter{}, err
	}
	at, err := in.At.filter("at")
	if err != nil {
		return store.OrderListFilter{}, err
	}
	page, err := listPage(in.Limit, in.Page)
	if err != nil {
		return store.OrderListFilter{}, err
	}
	var statuses []domain.OrderStatus
	for _, status := range in.Status {
		statuses = append(statuses, domain.OrderStatus(status))
	}
	return store.OrderListFilter{
		Venue:      venue,
		Account:    domain.AccountID(in.Account),
		Source:     domain.Source(in.Source),
		Side:       listSide(in.Side),
		Status:     statuses,
		BaseAsset:  in.BaseAsset.matcher(),
		QuoteAsset: in.QuoteAsset.matcher(),
		Amount:     in.Amount.filter(),
		Price:      in.Price.filter(),
		At:         at,
		Sort:       in.Sort.spec(),
		Page:       page,
	}, nil
}

func listOrdersHandler(
	src frameworkmcp.Source,
) frameworkmcp.ToolBody[listOrdersInput, listOrdersOutput] {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		in listOrdersInput,
	) (string, listOrdersOutput, error) {
		filter, err := in.filter()
		if err != nil {
			return "", listOrdersOutput{}, toolError(err)
		}
		if err := filter.Validate(); err != nil {
			return "", listOrdersOutput{}, toolError(err)
		}
		page, err := src.ListOrderRows(ctx, filter)
		if err != nil {
			return "", listOrdersOutput{}, toolError(err)
		}
		rows := make([]httpx.OrderDTO, 0, len(page.Rows))
		for _, row := range page.Rows {
			rows = append(rows, httpx.ToOrderRowDTO(row))
		}
		out := listOrdersOutput{Orders: rows, Total: page.Total}
		return fmt.Sprintf("%d of %d order(s)", len(rows), page.Total), out, nil
	}
}

type listTradesInput struct {
	Venue      *venueInput        `json:"venue,omitempty" jsonschema:"Trading-connection filter on the venue link of the trade's order"`
	Account    *textMatchInput    `json:"account,omitempty" jsonschema:"Account code match"`
	ID         string             `json:"id,omitempty" jsonschema:"Exact trade id"`
	BaseAsset  *textMatchInput    `json:"baseAsset,omitempty" jsonschema:"Base asset code match"`
	QuoteAsset *textMatchInput    `json:"quoteAsset,omitempty" jsonschema:"Quote asset code match"`
	Side       string             `json:"side,omitempty" jsonschema:"Trade side: buy | sell"`
	Source     string             `json:"source,omitempty" jsonschema:"Exact source channel: panel | api | mcp | system"`
	At         *timeRangeInput    `json:"at,omitempty" jsonschema:"Trade time range"`
	Quantity   *decimalRangeInput `json:"quantity,omitempty" jsonschema:"Fill quantity range"`
	Price      *decimalRangeInput `json:"price,omitempty" jsonschema:"Fill price range"`
	LockPrice  *decimalRangeInput `json:"lockPrice,omitempty" jsonschema:"Pre-trade lock price range"`
	Sort       *sortInput         `json:"sort,omitempty" jsonschema:"Sort order"`
	Limit      int                `json:"limit,omitempty" jsonschema:"Rows per page, default 50, capped at 500"`
	Page       int                `json:"page,omitempty" jsonschema:"1-based page number, default 1"`
}

type listTradesOutput struct {
	Trades []httpx.TradeDTO `json:"trades"`
	Total  int              `json:"total"`
}

func (in listTradesInput) filter() (store.TradeListFilter, error) {
	venue, err := in.Venue.filter()
	if err != nil {
		return store.TradeListFilter{}, err
	}
	var id domain.ExternalID
	if in.ID != "" {
		id, err = domain.ParseExternalID(in.ID)
		if err != nil {
			return store.TradeListFilter{}, fmt.Errorf("id: %w", err)
		}
	}
	at, err := in.At.filter("at")
	if err != nil {
		return store.TradeListFilter{}, err
	}
	page, err := listPage(in.Limit, in.Page)
	if err != nil {
		return store.TradeListFilter{}, err
	}
	return store.TradeListFilter{
		Venue:      venue,
		Account:    in.Account.matcher(),
		ExternalID: id,
		BaseAsset:  in.BaseAsset.matcher(),
		QuoteAsset: in.QuoteAsset.matcher(),
		Side:       listSide(in.Side),
		Source:     domain.Source(in.Source),
		At:         at,
		Quantity:   in.Quantity.filter(),
		Price:      in.Price.filter(),
		LockPrice:  in.LockPrice.filter(),
		Sort:       in.Sort.spec(),
		Page:       page,
	}, nil
}

func listTradesHandler(
	src frameworkmcp.Source,
) frameworkmcp.ToolBody[listTradesInput, listTradesOutput] {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		in listTradesInput,
	) (string, listTradesOutput, error) {
		filter, err := in.filter()
		if err != nil {
			return "", listTradesOutput{}, toolError(err)
		}
		if err := filter.Validate(); err != nil {
			return "", listTradesOutput{}, toolError(err)
		}
		page, err := src.ListTradeRows(ctx, filter)
		if err != nil {
			return "", listTradesOutput{}, toolError(err)
		}
		rows := make([]httpx.TradeDTO, 0, len(page.Rows))
		for _, row := range page.Rows {
			rows = append(rows, httpx.ToTradeRowDTO(row))
		}
		out := listTradesOutput{Trades: rows, Total: page.Total}
		return fmt.Sprintf("%d of %d trade(s)", len(rows), page.Total), out, nil
	}
}

// pnlInput is the JSON form of the account list's P&L filter,
// store.DenominatedDecimalRangeFilter.
type pnlInput struct {
	Currency string             `json:"currency,omitempty" jsonschema:"Currency the bounds are expressed in; required with any bound"`
	Range    *decimalRangeInput `json:"range,omitempty" jsonschema:"P&L bounds in currency"`
}

// filter returns the core P&L filter; an omitted filter is unrestricted.
func (p *pnlInput) filter() store.DenominatedDecimalRangeFilter {
	if p == nil {
		return store.DenominatedDecimalRangeFilter{}
	}
	return store.DenominatedDecimalRangeFilter{
		Range:    p.Range.filter(),
		Currency: p.Currency,
	}
}

type listAccountsInput struct {
	Group            *string          `json:"group,omitempty" jsonschema:"Exact group code; an empty string selects accounts in no group; omitted = any group"`
	Code             *textMatchInput  `json:"code,omitempty" jsonschema:"Account code or title match"`
	BlockReason      *textMatchInput  `json:"blockReason,omitempty" jsonschema:"Effective block reason match"`
	Status           string           `json:"status,omitempty" jsonschema:"Effective block state: active | blocked; omitted = both"`
	Position         *countRangeInput `json:"position,omitempty" jsonschema:"Open-position count range"`
	Pnl              *pnlInput        `json:"pnl,omitempty" jsonschema:"P&L bounds in one currency"`
	IncludeUnsetPnl  bool             `json:"includeUnsetPnl,omitempty" jsonschema:"Also keep accounts with no P&L yet; needs pnl.currency"`
	IncludeHaltedPnl bool             `json:"includeHaltedPnl,omitempty" jsonschema:"Also keep accounts whose P&L is halted; needs pnl.currency"`
	Venue            *venueInput      `json:"venue,omitempty" jsonschema:"Trading-connection filter on the account's own grants and its group's"`
	Sort             *sortInput       `json:"sort,omitempty" jsonschema:"Sort order"`
	Limit            int              `json:"limit,omitempty" jsonschema:"Rows per page, default 50, capped at 500"`
	Page             int              `json:"page,omitempty" jsonschema:"1-based page number, default 1"`
}

type listAccountsOutput struct {
	Accounts []httpx.AccountDTO `json:"accounts"`
	Total    int                `json:"total"`
}

func (in listAccountsInput) filter() (store.AccountListFilter, error) {
	venue, err := in.Venue.filter()
	if err != nil {
		return store.AccountListFilter{}, err
	}
	page, err := listPage(in.Limit, in.Page)
	if err != nil {
		return store.AccountListFilter{}, err
	}
	return store.AccountListFilter{
		GroupCode:        in.Group,
		Code:             in.Code.matcher(),
		BlockReason:      in.BlockReason.matcher(),
		Status:           store.StatusFilter(in.Status),
		Position:         in.Position.filter(),
		Pnl:              in.Pnl.filter(),
		IncludeUnsetPnl:  in.IncludeUnsetPnl,
		IncludeHaltedPnl: in.IncludeHaltedPnl,
		Venue:            venue,
		Sort:             in.Sort.spec(),
		Page:             page,
	}, nil
}

func listAccountsHandler(
	src frameworkmcp.Source,
) frameworkmcp.ToolBody[listAccountsInput, listAccountsOutput] {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		in listAccountsInput,
	) (string, listAccountsOutput, error) {
		filter, err := in.filter()
		if err != nil {
			return "", listAccountsOutput{}, toolError(err)
		}
		if err := filter.Validate(); err != nil {
			return "", listAccountsOutput{}, toolError(err)
		}
		page, err := backend.ListAccountRowsWithBlocks(ctx, src, filter)
		if err != nil {
			return "", listAccountsOutput{}, toolError(err)
		}
		rows := make([]httpx.AccountDTO, 0, len(page.Rows))
		for _, row := range page.Rows {
			rows = append(rows, httpx.ToAccountRowDTO(row.AccountListRow, row.Block))
		}
		out := listAccountsOutput{Accounts: rows, Total: page.Total}
		return fmt.Sprintf("%d of %d account(s)", len(rows), page.Total), out, nil
	}
}

type listGroupsInput struct {
	Code        *textMatchInput  `json:"code,omitempty" jsonschema:"Group code or title match"`
	Notes       *textMatchInput  `json:"notes,omitempty" jsonschema:"Group notes match"`
	BlockReason *textMatchInput  `json:"blockReason,omitempty" jsonschema:"Block reason match"`
	Status      string           `json:"status,omitempty" jsonschema:"Block state: active | blocked; omitted = both"`
	Position    *countRangeInput `json:"position,omitempty" jsonschema:"Open-position count range"`
	Account     *countRangeInput `json:"account,omitempty" jsonschema:"Member-account count range"`
	Venue       *venueInput      `json:"venue,omitempty" jsonschema:"Trading-connection filter on the group's own grants"`
	Sort        *sortInput       `json:"sort,omitempty" jsonschema:"Sort order"`
	Limit       int              `json:"limit,omitempty" jsonschema:"Rows per page, default 50, capped at 500"`
	Page        int              `json:"page,omitempty" jsonschema:"1-based page number, default 1"`
}

type listGroupsOutput struct {
	Groups []httpx.GroupDTO `json:"groups"`
	Total  int              `json:"total"`
}

func (in listGroupsInput) filter() (store.GroupListFilter, error) {
	venue, err := in.Venue.filter()
	if err != nil {
		return store.GroupListFilter{}, err
	}
	page, err := listPage(in.Limit, in.Page)
	if err != nil {
		return store.GroupListFilter{}, err
	}
	return store.GroupListFilter{
		Code:        in.Code.matcher(),
		Notes:       in.Notes.matcher(),
		BlockReason: in.BlockReason.matcher(),
		Status:      store.StatusFilter(in.Status),
		Position:    in.Position.filter(),
		Account:     in.Account.filter(),
		Venue:       venue,
		Sort:        in.Sort.spec(),
		Page:        page,
	}, nil
}

func listGroupsHandler(
	src frameworkmcp.Source,
) frameworkmcp.ToolBody[listGroupsInput, listGroupsOutput] {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		in listGroupsInput,
	) (string, listGroupsOutput, error) {
		filter, err := in.filter()
		if err != nil {
			return "", listGroupsOutput{}, toolError(err)
		}
		if err := filter.Validate(); err != nil {
			return "", listGroupsOutput{}, toolError(err)
		}
		page, err := src.ListGroupRows(ctx, filter)
		if err != nil {
			return "", listGroupsOutput{}, toolError(err)
		}
		rows := make([]httpx.GroupDTO, 0, len(page.Rows))
		// Total counts only real groups; the synthetic default group is
		// stored under the empty code and rides on the first page outside it.
		realGroups, hasDefault := 0, false
		for _, row := range page.Rows {
			rows = append(rows, httpx.ToGroupRowDTO(row))
			if row.Group.Code == "" {
				hasDefault = true
			} else {
				realGroups++
			}
		}
		out := listGroupsOutput{Groups: rows, Total: page.Total}
		text := fmt.Sprintf("%d of %d group(s)", realGroups, page.Total)
		if hasDefault {
			text += ", plus the default group"
		}
		return text, out, nil
	}
}
