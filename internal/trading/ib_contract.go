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

package trading

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/scmhub/ibapi"
	"github.com/shopspring/decimal"
	"go.openpit.dev/officer/framework/domain"
	fwtrading "go.openpit.dev/officer/framework/trading"
)

func ibNonAlphanumeric(ch rune) bool {
	return (ch < 'A' || ch > 'Z') && (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9')
}

func ibSymbolPart(s string) bool {
	if s == "" {
		return false
	}
	for _, ch := range s {
		if ibNonAlphanumeric(ch) && ch != '-' && ch != '_' {
			return false
		}
	}
	return true
}

func ibSymbolContracts(symbol, currency string) ([]*ibapi.Contract, error) {
	parts := strings.Split(symbol, ".")
	for _, part := range parts {
		if len(parts) == 1 && strings.Contains(part, " ") {
			class := strings.Split(part, " ")
			if len(class) == 2 && class[0] != "" && class[1] != "" && strings.IndexFunc(strings.Join(class, ""), ibNonAlphanumeric) == -1 {
				continue
			}
		}
		if !ibSymbolPart(part) {
			return nil, fmt.Errorf("%w: unsupported IB symbol shape", fwtrading.ErrUnsupported)
		}
	}
	contract := func(sec, exchange, quote string) *ibapi.Contract {
		c := ibapi.NewContract()
		c.Symbol, c.SecType, c.Exchange, c.Currency = parts[0], sec, exchange, quote
		return c
	}
	switch len(parts) {
	case 1:
		return []*ibapi.Contract{contract("STK", "SMART", currency)}, nil
	case 2:
		return []*ibapi.Contract{contract("CASH", "IDEALPRO", parts[1]), contract("CRYPTO", "PAXOS", parts[1])}, nil
	default:
		return nil, fmt.Errorf("%w: unsupported IB symbol shape", fwtrading.ErrUnsupported)
	}
}

func (s *ibTradingSession) resolve(ctx context.Context, symbol, currency string) ([]*ibapi.ContractDetails, error) {
	contracts, err := ibSymbolContracts(symbol, currency)
	if err != nil {
		return nil, err
	}
	var matches []*ibapi.ContractDetails
	for _, contract := range contracts {
		details, err := s.contractDetails(ctx, contract)
		if err != nil {
			return nil, err
		}
		for _, detail := range details {
			if detail.Contract.SecType == contract.SecType {
				matches = append(matches, detail)
			}
		}
	}
	return matches, nil
}

func ibTradable(detail *ibapi.ContractDetails) bool {
	// ContractDetails has no tradable boolean. OrderTypes advertises the
	// supported order types; an empty set cannot be traded by this connector.
	return strings.TrimSpace(detail.OrderTypes) != ""
}

func (s *ibTradingSession) orderContract(ctx context.Context, order fwtrading.Order) (*ibapi.Contract, error) {
	if order.BaseAsset == "" || order.QuoteAsset == "" {
		return nil, fmt.Errorf("%w: mapping base and quote assets are required", fwtrading.ErrUnsupported)
	}
	if parts := strings.Split(order.Symbol, "."); len(parts) == 2 {
		// Pair symbols and Officer assets are compared case-insensitively.
		if !strings.EqualFold(parts[0], order.BaseAsset) || !strings.EqualFold(parts[1], order.QuoteAsset) {
			return nil, fmt.Errorf("%w: BASE.QUOTE differs from mapping assets (case-insensitive comparison)", fwtrading.ErrUnsupported)
		}
	}
	details, err := s.resolve(ctx, order.Symbol, order.QuoteAsset)
	if err != nil {
		return nil, err
	}
	if len(details) != 1 {
		return nil, fmt.Errorf("%w: IB contract resolution returned %d matches", fwtrading.ErrRejected, len(details))
	}
	if !ibTradable(details[0]) {
		return nil, fmt.Errorf("%w: IB contract has no supported order types", fwtrading.ErrRejected)
	}
	result := details[0].Contract
	// Keep the requested routing venue even if details name a primary venue.
	switch result.SecType {
	case "STK":
		result.Exchange = "SMART"
	case "CASH":
		result.Exchange = "IDEALPRO"
	case "CRYPTO":
		result.Exchange = "PAXOS"
	}
	if !strings.EqualFold(result.Currency, order.QuoteAsset) {
		return nil, fmt.Errorf("%w: contract currency differs from mapping quote asset", fwtrading.ErrRejected)
	}
	return &result, nil
}

func ibOrder(order fwtrading.Order, contract *ibapi.Contract) (*ibapi.Order, error) {
	tif, err := parseTimeInForceRoute(order.Route, "IB", "day", "gtc", "ioc")
	if err != nil {
		return nil, err
	}
	// CashQty does not specify the exact total base quantity required for
	// leaves and finality. Never invent it for a cash-sized market order.
	if order.AmountKind != domain.OrderAmountKindQuantity {
		return nil, fmt.Errorf("%w: IB requires quantity orders; cashQty has no exact total base quantity", fwtrading.ErrUnsupported)
	}
	if contract.SecType == "CRYPTO" && order.Side == domain.OrderSideBuy && order.LimitPrice == "" {
		return nil, fmt.Errorf("%w: IB crypto market buy requires cash quantity", fwtrading.ErrUnsupported)
	}
	if order.Side != domain.OrderSideBuy && order.Side != domain.OrderSideSell {
		return nil, fmt.Errorf("%w: unsupported side", fwtrading.ErrRejected)
	}
	qty, err := positiveDecimal(order.Quantity, "quantity")
	if err != nil {
		return nil, err
	}
	ibQty, err := ibapi.StringToDecimalErr(qty.String())
	if err != nil {
		return nil, fmt.Errorf("%w: quantity cannot be represented by IB", fwtrading.ErrUnsupported)
	}
	roundtrip, err := decimal.NewFromString(ibQty.String())
	if err != nil || !roundtrip.Equal(qty) {
		return nil, fmt.Errorf("%w: quantity exceeds IB precision", fwtrading.ErrUnsupported)
	}
	result := ibapi.NewOrder()
	result.OrderRef, result.Account = order.ClientOrderID, order.VenueAccount
	result.Action, result.TIF = strings.ToUpper(string(order.Side)), strings.ToUpper(tif)
	result.OrderType, result.TotalQuantity = "MKT", ibQty
	if order.LimitPrice != "" {
		px, err := positiveDecimal(order.LimitPrice, "limit price")
		if err != nil {
			return nil, err
		}
		value, _ := px.Float64()
		if math.IsInf(value, 0) || !decimal.NewFromFloat(value).Equal(px) {
			return nil, fmt.Errorf("%w: limit price exceeds IB precision", fwtrading.ErrUnsupported)
		}
		result.OrderType, result.LmtPrice = "LMT", value
	}
	return result, nil
}

// PreCheck resolves the exact mapped contract before Officer reserves funds.
func (c *ibConnector) PreCheck(ctx context.Context, order fwtrading.Order) error {
	if _, err := parseTimeInForceRoute(order.Route, "IB", "day", "gtc", "ioc"); err != nil {
		return err
	}
	s, err := c.liveSession()
	if err != nil {
		return err
	}
	accounts, err := s.accounts(ctx)
	if err != nil {
		return err
	}
	if err := c.checkMode(accounts); err != nil {
		return fmt.Errorf("%w: %v", fwtrading.ErrRejected, err)
	}
	managed := false
	for _, account := range accounts {
		if account == order.VenueAccount {
			managed = true
		}
	}
	if !managed {
		return fmt.Errorf("%w: VenueAccount is not a managed account", fwtrading.ErrRejected)
	}
	contract, err := s.orderContract(ctx, order)
	if err != nil {
		return err
	}
	_, err = ibOrder(order, contract)
	return err
}

// VerifySymbol shows quote currencies so operators can choose a mapping.
func (c *ibConnector) VerifySymbol(ctx context.Context, external string) (result fwtrading.SymbolVerification, err error) {
	if _, err := ibSymbolContracts(external, ""); err != nil {
		return result, err
	}
	err = c.probe(ctx, func(ctx context.Context, s *ibTradingSession) error {
		details, err := s.resolve(ctx, external, "")
		if err != nil {
			return err
		}
		count := len(details)
		if strings.Contains(external, ".") && count != 1 {
			result.Details = fmt.Sprintf("IB contract resolution returned %d matches", count)
			return nil
		}
		result.Exists = count > 0
		labels := make(map[string]bool)
		for _, d := range details {
			result.Tradable = result.Tradable || ibTradable(d)
			labels[d.Contract.Currency+" "+d.Contract.PrimaryExchange] = true
		}
		var sorted []string
		for label := range labels {
			sorted = append(sorted, label)
		}
		sort.Strings(sorted)
		result.Details = strings.Join(sorted, ", ")
		return nil
	})
	return
}

// SearchSymbols resolves supported spot classes without subscribing to quotes.
func (c *ibConnector) SearchSymbols(ctx context.Context, query string) (matches []fwtrading.SymbolMatch, err error) {
	matches = []fwtrading.SymbolMatch{}
	query = strings.TrimSpace(query)
	if query == "" {
		return matches, nil
	}
	if _, err := ibSymbolContracts(query, ""); err != nil {
		return matches, nil
	}
	err = c.probe(ctx, func(ctx context.Context, s *ibTradingSession) error {
		details, err := s.resolve(ctx, query, "")
		if err != nil {
			return err
		}
		seen := make(map[string]bool)
		for _, d := range details {
			match := fwtrading.SymbolMatch{Symbol: d.Contract.Symbol, Name: d.LongName, Class: d.Contract.SecType, Exchange: d.Contract.Exchange, Tradable: ibTradable(d)}
			switch d.Contract.SecType {
			case "CASH", "CRYPTO":
				match.Symbol += "." + d.Contract.Currency
				match.BaseAsset, match.QuoteAsset = d.Contract.Symbol, d.Contract.Currency
			case "STK":
				match.Exchange = d.Contract.PrimaryExchange
			}
			key := match.Symbol + ":" + strconv.FormatInt(d.Contract.ConID, 10)
			if seen[key] {
				continue
			}
			seen[key] = true
			matches = append(matches, match)
		}
		return nil
	})
	return
}
