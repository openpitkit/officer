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
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	fwtrading "go.openpit.dev/officer/framework/trading"
)

const alpacaFeePageSize = 100

type alpacaFeeActivity struct {
	ID          string    `json:"id"`
	Type        string    `json:"activity_type"`
	CreatedAt   time.Time `json:"created_at"`
	Currency    string    `json:"currency"`
	NetAmount   string    `json:"net_amount"`
	Quantity    string    `json:"qty"`
	Symbol      string    `json:"symbol"`
	Status      string    `json:"status"`
	Description string    `json:"description"`
}

func (c *alpacaConnector) Fees(ctx context.Context, since time.Time, after string) ([]fwtrading.Fee, string, error) {
	query := url.Values{
		"activity_types": {"FEE,CFEE"}, "after": {since.Format(time.RFC3339Nano)},
		"direction": {"asc"}, "page_size": {"100"},
	}
	if after != "" {
		query.Set("page_token", after)
	}
	var page []alpacaFeeActivity
	_, err := c.client.Request(ctx, http.MethodGet, "/v2/account/activities?"+query.Encode(), nil, &page)
	if err != nil {
		return nil, "", err
	}
	if after != "" {
		for _, activity := range page {
			if activity.ID == after {
				return nil, "", errors.New("alpaca: fee pagination did not advance")
			}
		}
	}
	fees := make([]fwtrading.Fee, 0, len(page))
	previous := after
	for _, activity := range page {
		if activity.ID == "" || activity.ID == previous {
			return fees, "", errors.New("alpaca: fee pagination did not advance")
		}
		fee, err := c.feeActivity(ctx, activity)
		if err != nil {
			return fees, "", err
		}
		fees = append(fees, fee)
		previous = activity.ID
	}
	if len(page) < alpacaFeePageSize {
		return fees, "", nil
	}
	return fees, previous, nil
}

func (c *alpacaConnector) feeActivity(ctx context.Context, activity alpacaFeeActivity) (fwtrading.Fee, error) {
	fee := fwtrading.Fee{ID: activity.ID, Status: fwtrading.FeeNotApplicable,
		Detail: c.redactStreamMessage(activity.Description), At: activity.CreatedAt}
	notApplicable := func(reason string) (fwtrading.Fee, error) {
		fee.Detail = c.redactStreamMessage(reason) + ": " + fee.Detail
		return fee, nil
	}
	if activity.Status != "executed" {
		return notApplicable("activity status " + activity.Status)
	}
	if activity.ID == "" {
		return notApplicable("missing activity id")
	}
	net, err := parseAlpacaFeeDecimal(activity.NetAmount)
	if err != nil {
		return notApplicable("invalid net_amount")
	}
	switch activity.Type {
	case "FEE":
		if activity.Currency == "" || net.IsZero() {
			return notApplicable("FEE requires currency and non-zero net_amount")
		}
		fee.Asset, fee.Amount = activity.Currency, activity.NetAmount
	case "CFEE":
		qty, err := parseAlpacaFeeDecimal(activity.Quantity)
		if err != nil {
			return notApplicable("invalid qty")
		}
		switch {
		case !qty.IsZero() && net.IsZero():
			if activity.Symbol == "" {
				return notApplicable("CFEE requires symbol for quantity charge")
			}
			c.feeMu.Lock()
			defer c.feeMu.Unlock()
			symbol, cached := c.feeSymbols[activity.Symbol]
			if !cached {
				asset, found, err := c.client.Asset(ctx, activity.Symbol)
				if err != nil {
					return fwtrading.Fee{}, err
				}
				if c.feeSymbols == nil {
					c.feeSymbols = make(map[string]string)
				}
				if !found {
					c.feeSymbols[activity.Symbol] = ""
					return notApplicable("CFEE asset must resolve to BASE/QUOTE")
				}
				symbol = asset.Symbol
				c.feeSymbols[activity.Symbol] = symbol
			}
			if !strings.Contains(symbol, "/") {
				return notApplicable("CFEE asset must resolve to BASE/QUOTE")
			}
			base, _, err := c.InstrumentAssets(symbol)
			if err != nil {
				return notApplicable("CFEE asset has malformed BASE/QUOTE symbol")
			}
			fee.Asset, fee.Amount = base, activity.Quantity
		case qty.IsZero() && !net.IsZero():
			if activity.Currency == "" {
				return notApplicable("CFEE money charge requires currency")
			}
			fee.Asset, fee.Amount = activity.Currency, activity.NetAmount
		default:
			return notApplicable("CFEE requires exactly one non-zero qty or net_amount")
		}
	default:
		return notApplicable("unsupported activity type " + activity.Type)
	}
	fee.Status = fwtrading.FeeExecuted
	return fee, nil
}

// Empty values are explicitly allowed for the unused leg of a CFEE activity.
func parseAlpacaFeeDecimal(raw string) (decimal.Decimal, error) {
	if raw == "" {
		return decimal.Zero, nil
	}
	if len(raw) > alpacaDecimalMaxLength {
		return decimal.Decimal{}, errors.New("alpaca: fee decimal too long")
	}
	if raw != strings.TrimSpace(raw) || strings.ContainsAny(raw, "eE") {
		return decimal.Decimal{}, errors.New("alpaca: fee amount must be a plain decimal")
	}
	return decimal.NewFromString(raw)
}

func (c *alpacaConnector) InstrumentAssets(symbol string) (base, quote string, err error) {
	if symbol == "" || strings.ContainsAny(symbol, " \t\r\n") {
		return "", "", errors.New("alpaca: instrument symbol is required without whitespace")
	}
	if !strings.Contains(symbol, "/") {
		return symbol, "USD", nil
	}
	parts := strings.Split(symbol, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", errors.New("alpaca: instrument symbol must be BASE/QUOTE")
	}
	return parts[0], parts[1], nil
}

var _ fwtrading.FeeReader = (*alpacaConnector)(nil)
