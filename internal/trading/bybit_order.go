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
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"time"

	"github.com/shopspring/decimal"
	"go.openpit.dev/officer/framework/domain"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/bybit"
)

var (
	errBybitExtraFees   = errors.New("bybit: unsupported extra fees")
	errBybitOrderStatus = errors.New("bybit: unknown order status")
)

type bybitOrderRequest struct {
	Category    string `json:"category"`
	Symbol      string `json:"symbol"`
	Side        string `json:"side"`
	OrderType   string `json:"orderType"`
	Qty         string `json:"qty"`
	OrderLinkID string `json:"orderLinkId"`
	IsLeverage  int    `json:"isLeverage"`
	OrderFilter string `json:"orderFilter"`
	Price       string `json:"price,omitempty"`
	TimeInForce string `json:"timeInForce,omitempty"`
	MarketUnit  string `json:"marketUnit,omitempty"`
}

func buildBybitOrderRequest(order fwtrading.Order) (bybitOrderRequest, error) {
	limit := order.LimitPrice != ""
	tif, err := parseSpotRoute(order.Route, "Bybit", limit)
	if err != nil {
		return bybitOrderRequest{}, err
	}
	if order.AmountKind != domain.OrderAmountKindQuantity {
		return bybitOrderRequest{}, fmt.Errorf(
			"%w: Bybit requires quantity orders", fwtrading.ErrUnsupported)
	}
	side, ok := bybitOrderSide(order.Side)
	if !ok {
		return bybitOrderRequest{}, fmt.Errorf("%w: unsupported order side", fwtrading.ErrRejected)
	}
	request := bybitOrderRequest{
		Category: "spot", Symbol: order.Symbol, Side: side,
		OrderType: "Market", Qty: order.Quantity,
		OrderLinkID: order.ClientOrderID, IsLeverage: 0,
		OrderFilter: "Order", MarketUnit: "baseCoin",
	}
	if limit {
		request.OrderType = "Limit"
		request.Price = order.LimitPrice
		request.TimeInForce = tif
		request.MarketUnit = ""
	}
	return request, nil
}

func bybitOrderSide(side domain.OrderSide) (string, bool) {
	switch side {
	case domain.OrderSideBuy:
		return "Buy", true
	case domain.OrderSideSell:
		return "Sell", true
	default:
		return "", false
	}
}

func bybitDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func bybitMilliseconds(value string) (int64, bool) {
	if !bybitDigits(value) {
		return 0, false
	}
	ms, err := strconv.ParseInt(value, 10, 64)
	return ms, err == nil && ms > 0
}

type bybitOrder struct {
	OrderID      string `json:"orderId"`
	OrderLinkID  string `json:"orderLinkId"`
	Symbol       string `json:"symbol"`
	Side         string `json:"side"`
	OrderStatus  string `json:"orderStatus"`
	Qty          string `json:"qty"`
	LeavesQty    string `json:"leavesQty"`
	CumExecQty   string `json:"cumExecQty"`
	RejectReason string `json:"rejectReason"`
	CreatedTime  string `json:"createdTime"`
}

func bybitCreateOrderAck(order bybitOrder, clientID string) (fwtrading.Ack, error) {
	if order.OrderID == "" || order.OrderLinkID != clientID {
		return fwtrading.Ack{}, errors.New(
			"bybit: send outcome unknown: unusable acknowledgement")
	}
	return fwtrading.Ack{VenueOrderID: order.OrderID}, nil
}

func bybitSendError(err error) error {
	var apiErr *bybit.APIError
	if errors.As(err, &apiErr) && apiErr.BodyDecoded && apiErr.Status < 500 {
		// Only documented pre-creation refusals release the reservation.
		// Timeouts, duplicate IDs and unspecified failures remain unknown.
		switch apiErr.RetCode {
		case 10001, 10002, 10003, 10004, 10005, 10006, 10010,
			170005, 170105, 170115, 170116, 170117, 170121, 170124,
			170130, 170131, 170132, 170133, 170134, 170136, 170137,
			170140, 170148, 170151, 170157, 170159, 170192, 170193,
			170194, 170209, 170210, 170217, 170218, 170222, 170224,
			170227, 170228, 170371, 170372, 170381, 170382:
			return fmt.Errorf("%w: %w", fwtrading.ErrRejected, err)
		}
	}
	return fmt.Errorf("bybit: send outcome unknown: %w", err)
}

func checkBybitRules(order fwtrading.Order, rules bybit.InstrumentRules) error {
	if rules.Symbol != order.Symbol {
		return fmt.Errorf("bybit: instrument symbol mismatch: %w", domain.ErrUpstream)
	}
	if rules.Status != "Trading" {
		return fmt.Errorf("%w: symbol must allow spot trading", fwtrading.ErrRejected)
	}
	qty, err := parsePlainDecimal(order.Quantity, true)
	if err != nil {
		return fmt.Errorf("%w: quantity must be a positive plain decimal",
			fwtrading.ErrRejected)
	}
	limit := order.LimitPrice != ""
	var price decimal.Decimal
	if limit {
		price, err = parsePlainDecimal(order.LimitPrice, true)
		if err != nil {
			return fmt.Errorf("%w: price must be a positive plain decimal",
				fwtrading.ErrRejected)
		}
	}
	read := func(name, raw string) (decimal.Decimal, error) {
		value, err := parsePlainDecimal(raw, true)
		if err != nil {
			return decimal.Decimal{}, fmt.Errorf("bybit: invalid %s: %w",
				name, domain.ErrUpstream)
		}
		return value, nil
	}
	increment, err := read("basePrecision", rules.LotSizeFilter.BasePrecision)
	if err != nil {
		return err
	}
	maxName, maxRaw := "maxMarketOrderQty", rules.LotSizeFilter.MaxMarketOrderQty
	if limit {
		maxName, maxRaw = "maxLimitOrderQty", rules.LotSizeFilter.MaxLimitOrderQty
	}
	maximum, err := read(maxName, maxRaw)
	if err != nil {
		return err
	}
	if !qty.Mod(increment).IsZero() {
		return fmt.Errorf("%w: quantity is not a basePrecision increment",
			fwtrading.ErrRejected)
	}
	if qty.GreaterThan(maximum) {
		return fmt.Errorf("%w: quantity exceeds %s", fwtrading.ErrRejected, maxName)
	}
	if !limit {
		return nil
	}
	tick, err := read("tickSize", rules.PriceFilter.TickSize)
	if err != nil {
		return err
	}
	minimum, err := read("minOrderAmt", rules.LotSizeFilter.MinOrderAmt)
	if err != nil {
		return err
	}
	if !price.Mod(tick).IsZero() {
		return fmt.Errorf("%w: price is not a tickSize increment", fwtrading.ErrRejected)
	}
	if qty.Mul(price).LessThan(minimum) {
		return fmt.Errorf("%w: notional is below minOrderAmt", fwtrading.ErrRejected)
	}
	return nil
}

func bybitVenueStatus(status string) (fwtrading.VenueStatus, bool) {
	switch status {
	case "New", "PartiallyFilled":
		return fwtrading.VenueStatusOpen, true
	case "Filled":
		return fwtrading.VenueStatusFilled, true
	case "Cancelled", "PartiallyFilledCanceled", "Rejected":
		return fwtrading.VenueStatusCancelled, true
	default:
		return "", false
	}
}

type bybitExecution struct {
	Symbol      string          `json:"symbol"`
	OrderID     string          `json:"orderId"`
	OrderLinkID string          `json:"orderLinkId"`
	Side        string          `json:"side"`
	OrderQty    string          `json:"orderQty"`
	LeavesQty   string          `json:"leavesQty"`
	ExecID      string          `json:"execId"`
	ExecPrice   string          `json:"execPrice"`
	ExecQty     string          `json:"execQty"`
	ExecFee     string          `json:"execFee"`
	FeeCurrency string          `json:"feeCurrency"`
	ExecType    string          `json:"execType"`
	ExecTime    string          `json:"execTime"`
	ExtraFees   json.RawMessage `json:"extraFees"`
}

type bybitFill struct {
	fill       fwtrading.Fill
	cumulative decimal.Decimal
}

func (execution bybitExecution) fill(base, quote string) (bybitFill, error) {
	if execution.ExecType != "Trade" || execution.ExecID == "" {
		return bybitFill{}, fmt.Errorf("bybit: unreadable execution: %w",
			domain.ErrUpstream)
	}
	ms, ok := bybitMilliseconds(execution.ExecTime)
	if !ok {
		return bybitFill{}, fmt.Errorf("bybit: unreadable execution time: %w",
			domain.ErrUpstream)
	}
	qty, qtyErr := parsePlainDecimal(execution.ExecQty, true)
	_, priceErr := parsePlainDecimal(execution.ExecPrice, true)
	orderQty, orderErr := parsePlainDecimal(execution.OrderQty, true)
	leaves, leavesErr := parsePlainDecimal(execution.LeavesQty, false)
	if qtyErr != nil || priceErr != nil || orderErr != nil || leavesErr != nil {
		return bybitFill{}, fmt.Errorf("bybit: unreadable execution quantities: %w",
			domain.ErrUpstream)
	}
	cum := orderQty.Sub(leaves)
	if qty.GreaterThan(cum) {
		return bybitFill{}, fmt.Errorf("bybit: inconsistent execution quantities: %w",
			domain.ErrUpstream)
	}
	if len(execution.ExtraFees) != 0 {
		var compact bytes.Buffer
		if err := json.Compact(&compact, execution.ExtraFees); err != nil {
			return bybitFill{}, fmt.Errorf("%w: %w",
				errBybitExtraFees, domain.ErrUpstream)
		}
		switch compact.String() {
		case "null", `""`, "[]":
		default:
			return bybitFill{}, fmt.Errorf("%w: %w",
				errBybitExtraFees, domain.ErrUpstream)
		}
	}
	commission, err := spotCommission(execution.ExecFee, execution.FeeCurrency)
	if err != nil {
		return bybitFill{}, fmt.Errorf("bybit: unreadable commission: %w",
			domain.ErrUpstream)
	}
	fill := fwtrading.Fill{
		Quantity: execution.ExecQty, Price: execution.ExecPrice,
		CumQuantity: cum.String(), LeavesQuantity: leaves.String(),
		Final: leaves.IsZero(), At: time.UnixMilli(ms).UTC(),
	}
	if commission != nil {
		if base == "" || quote == "" {
			return bybitFill{}, fmt.Errorf("bybit: symbol assets are missing: %w",
				domain.ErrUpstream)
		}
		fill.Commission = commission
		fill.CommissionLeg = commissionLeg(execution.FeeCurrency, base, quote)
	}
	return bybitFill{fill: fill, cumulative: cum}, nil
}

func (order bybitOrder) snapshot(executions []bybitExecution,
	base, quote string,
) (fwtrading.Snapshot, error) {
	status, known := bybitVenueStatus(order.OrderStatus)
	if !known {
		return fwtrading.Snapshot{}, fmt.Errorf("%w: %w",
			errBybitOrderStatus, domain.ErrUpstream)
	}
	if order.Symbol == "" || order.Side == "" {
		return fwtrading.Snapshot{}, fmt.Errorf("bybit: missing order identity: %w",
			domain.ErrUpstream)
	}
	qty, qtyErr := parsePlainDecimal(order.Qty, true)
	filled, filledErr := parsePlainDecimal(order.CumExecQty, false)
	if order.OrderID == "" || qtyErr != nil || filledErr != nil ||
		filled.GreaterThan(qty) ||
		(status == fwtrading.VenueStatusFilled && !filled.Equal(qty)) {
		return fwtrading.Snapshot{}, fmt.Errorf("bybit: unreadable order quantities: %w",
			domain.ErrUpstream)
	}
	var fills []bybitFill
	seen := make(map[string]bybitExecution)
	for _, execution := range executions {
		orderQty, err := parsePlainDecimal(execution.OrderQty, true)
		if execution.OrderID != order.OrderID ||
			execution.Symbol != order.Symbol || execution.Side != order.Side ||
			err != nil || !orderQty.Equal(qty) {
			return fwtrading.Snapshot{}, fmt.Errorf("bybit: execution order mismatch: %w",
				domain.ErrUpstream)
		}
		if previous, exists := seen[execution.ExecID]; exists {
			if !reflect.DeepEqual(previous, execution) {
				return fwtrading.Snapshot{}, fmt.Errorf("bybit: conflicting execution id: %w",
					domain.ErrUpstream)
			}
			continue
		}
		fill, err := execution.fill(base, quote)
		if err != nil {
			return fwtrading.Snapshot{}, err
		}
		seen[execution.ExecID] = execution
		fills = append(fills, fill)
	}
	// Reconcile validates continuity and completeness of the sorted fills.
	sort.Slice(fills, func(i, j int) bool {
		return fills[i].cumulative.LessThan(fills[j].cumulative)
	})
	snapshot := fwtrading.Snapshot{
		VenueOrderID: order.OrderID, Status: status,
		FilledQuantity: order.CumExecQty, Reason: order.RejectReason,
	}
	if snapshot.Reason == "EC_NoError" {
		snapshot.Reason = ""
	}
	for _, converted := range fills {
		snapshot.Fills = append(snapshot.Fills, converted.fill)
	}
	return snapshot, nil
}
