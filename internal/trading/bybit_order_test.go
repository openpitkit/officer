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
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"go.openpit.dev/officer/framework/domain"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/bybit"
)

func bybitTestOrder() fwtrading.Order {
	return fwtrading.Order{
		ClientOrderID: "client-1", Symbol: "BTCUSDT",
		Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity,
		Quantity: "2.000", Route: `{}`,
	}
}

func assertBybitError(t *testing.T, err, want error) {
	t.Helper()
	for _, class := range []error{
		fwtrading.ErrRejected, fwtrading.ErrUnsupported, domain.ErrUpstream,
	} {
		if errors.Is(err, class) != (want == class) {
			t.Fatalf("error = %v; class %v, want class %v", err, class, want)
		}
	}
	if want != nil && err == nil {
		t.Fatal("expected an error")
	}
}

func TestBybitOrderRequest(t *testing.T) {
	for _, tc := range []struct {
		name, price, route, want string
		side                     domain.OrderSide
	}{
		{"market-buy", "", `{}`, `{"category":"spot","symbol":"BTCUSDT","side":"Buy","orderType":"Market","qty":"2.000","orderLinkId":"client-1","isLeverage":0,"orderFilter":"Order","marketUnit":"baseCoin"}`, domain.OrderSideBuy},
		{"limit-sell", "2.50", `{"timeInForce":"GTC"}`, `{"category":"spot","symbol":"BTCUSDT","side":"Sell","orderType":"Limit","qty":"2.000","orderLinkId":"client-1","isLeverage":0,"orderFilter":"Order","price":"2.50","timeInForce":"GTC"}`, domain.OrderSideSell},
	} {
		t.Run(tc.name, func(t *testing.T) {
			order := bybitTestOrder()
			order.Side, order.LimitPrice, order.Route = tc.side, tc.price, tc.route
			request, err := buildBybitOrderRequest(order)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			var got, want map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("request = %s, want %s", raw, tc.want)
			}
		})
	}
	for _, tif := range []string{"GTC", "IOC", "FOK"} {
		order := bybitTestOrder()
		order.LimitPrice, order.Route = "1", fmt.Sprintf(`{"timeInForce":%q}`, tif)
		request, err := buildBybitOrderRequest(order)
		if err != nil || request.TimeInForce != tif {
			t.Fatalf("timeInForce %s: request=%+v err=%v", tif, request, err)
		}
	}
	for _, tc := range []struct {
		name   string
		change func(*fwtrading.Order)
		want   error
	}{
		{"not-json", func(o *fwtrading.Order) { o.Route = "{" }, fwtrading.ErrRejected},
		{"null-route", func(o *fwtrading.Order) { o.Route = "null" }, fwtrading.ErrRejected},
		{"array-route", func(o *fwtrading.Order) { o.Route = "[]" }, fwtrading.ErrRejected},
		{"unknown-field", func(o *fwtrading.Order) { o.Route = `{"foo":"bar"}` }, fwtrading.ErrRejected},
		{"market-tif", func(o *fwtrading.Order) { o.Route = `{"timeInForce":"IOC"}` }, fwtrading.ErrRejected},
		{"missing-tif", func(o *fwtrading.Order) { o.LimitPrice = "1" }, fwtrading.ErrRejected},
		{"invalid-tif", func(o *fwtrading.Order) { o.LimitPrice, o.Route = "1", `{"timeInForce":"DAY"}` }, fwtrading.ErrRejected},
		{"typed-tif", func(o *fwtrading.Order) { o.Route = `{"timeInForce":1}` }, fwtrading.ErrRejected},
		{"amount-kind", func(o *fwtrading.Order) { o.AmountKind = "notional" }, fwtrading.ErrUnsupported},
		{"missing-amount-kind", func(o *fwtrading.Order) { o.AmountKind = "" }, fwtrading.ErrUnsupported},
		{"side", func(o *fwtrading.Order) { o.Side = "Buy" }, fwtrading.ErrRejected},
		{"missing-side", func(o *fwtrading.Order) { o.Side = "" }, fwtrading.ErrRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			order := bybitTestOrder()
			tc.change(&order)
			_, err := buildBybitOrderRequest(order)
			assertBybitError(t, err, tc.want)
		})
	}
}

func TestBybitCreateOrderAck(t *testing.T) {
	for _, tc := range []struct {
		name, id, link string
		valid          bool
	}{
		{"valid", "venue-1", "client-1", true},
		{"missing-order", "", "client-1", false},
		{"missing-link", "venue-1", "", false},
		{"other-client", "venue-1", "client-2", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ack, err := bybitCreateOrderAck(bybitOrder{
				OrderID: tc.id, OrderLinkID: tc.link,
			}, "client-1")
			assertBybitError(t, err, nil)
			if tc.valid {
				if err != nil || ack.VenueOrderID != tc.id {
					t.Fatalf("ack=%+v err=%v", ack, err)
				}
			} else if err == nil || err.Error() != "bybit: send outcome unknown: unusable acknowledgement" || ack != (fwtrading.Ack{}) {
				t.Fatalf("ack=%+v err=%v", ack, err)
			}
		})
	}
}

func TestBybitSendError(t *testing.T) {
	definitive := []int{
		10001, 10002, 10003, 10004, 10005, 10006, 10010,
		170005, 170105, 170115, 170116, 170117, 170121, 170124,
		170130, 170131, 170132, 170133, 170134, 170136, 170137,
		170140, 170148, 170151, 170157, 170159, 170192, 170193,
		170194, 170209, 170210, 170217, 170218, 170222, 170224,
		170227, 170228, 170371, 170372, 170381, 170382,
	}
	for _, code := range definitive {
		for _, status := range []int{200, 400, 403, 429, 499} {
			t.Run(fmt.Sprintf("refused-%d-http-%d", code, status), func(t *testing.T) {
				original := &bybit.APIError{RetCode: code, Status: status, BodyDecoded: true}
				for _, err := range []error{original, fmt.Errorf("wrapped: %w", original)} {
					got := bybitSendError(err)
					assertBybitError(t, got, fwtrading.ErrRejected)
					if !errors.Is(got, original) {
						t.Fatal("lost API error")
					}
				}
			})
		}
		for _, apiErr := range []*bybit.APIError{
			{RetCode: code, Status: 200},
			{RetCode: code, Status: 500, BodyDecoded: true},
			{RetCode: code, Status: 503, BodyDecoded: true},
		} {
			got := bybitSendError(apiErr)
			assertBybitError(t, got, nil)
			if !strings.HasPrefix(got.Error(), "bybit: send outcome unknown: ") || !errors.Is(got, apiErr) {
				t.Fatalf("unknown error = %v", got)
			}
		}
	}
	for _, code := range []int{
		170007, 170035, 170141, 170146, 170001, 170032, 170149,
		170195, 170196, 10000, 10016, 0, 999999,
	} {
		t.Run(fmt.Sprintf("unknown-%d", code), func(t *testing.T) {
			original := &bybit.APIError{RetCode: code, Status: 400, BodyDecoded: true}
			got := bybitSendError(original)
			assertBybitError(t, got, nil)
			if !strings.HasPrefix(got.Error(), "bybit: send outcome unknown: ") || !errors.Is(got, original) {
				t.Fatalf("unknown error = %v", got)
			}
		})
	}
	original := errors.New("transport failure")
	got := bybitSendError(original)
	assertBybitError(t, got, nil)
	if !errors.Is(got, original) || !strings.HasPrefix(got.Error(), "bybit: send outcome unknown: ") {
		t.Fatalf("transport error = %v", got)
	}
}

func bybitTestRules() bybit.InstrumentRules {
	return bybit.InstrumentRules{
		Instrument: bybit.Instrument{Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT", Status: "Trading"},
		LotSizeFilter: bybit.LotSizeFilter{
			BasePrecision: "0.001", QuotePrecision: "0.01", MinOrderAmt: "5",
			MaxLimitOrderQty: "10", MaxMarketOrderQty: "3",
		},
		PriceFilter: bybit.PriceFilter{TickSize: "0.05"},
	}
}

func TestBybitRules(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*fwtrading.Order, *bybit.InstrumentRules)
		want   error
		rule   string
	}{
		{"limit-boundary", func(*fwtrading.Order, *bybit.InstrumentRules) {}, nil, ""},
		{"max-limit", func(o *fwtrading.Order, _ *bybit.InstrumentRules) { o.Quantity = "10" }, nil, ""},
		{"max-market", func(o *fwtrading.Order, _ *bybit.InstrumentRules) { o.LimitPrice, o.Quantity = "", "3" }, nil, ""},
		{"tiny-market", func(o *fwtrading.Order, _ *bybit.InstrumentRules) { o.LimitPrice, o.Quantity = "", "0.001" }, nil, ""},
		{"symbol", func(_ *fwtrading.Order, r *bybit.InstrumentRules) { r.Symbol = "ETHUSDT" }, domain.ErrUpstream, ""},
		{"not-trading", func(_ *fwtrading.Order, r *bybit.InstrumentRules) { r.Status = "Closed" }, fwtrading.ErrRejected, ""},
		{"missing-status", func(_ *fwtrading.Order, r *bybit.InstrumentRules) { r.Status = "" }, fwtrading.ErrRejected, ""},
		{"base-step", func(o *fwtrading.Order, _ *bybit.InstrumentRules) { o.Quantity = "2.0001" }, fwtrading.ErrRejected, "basePrecision"},
		{"limit-max", func(o *fwtrading.Order, _ *bybit.InstrumentRules) { o.Quantity = "10.001" }, fwtrading.ErrRejected, "maxLimitOrderQty"},
		{"market-max", func(o *fwtrading.Order, _ *bybit.InstrumentRules) { o.LimitPrice, o.Quantity = "", "3.001" }, fwtrading.ErrRejected, "maxMarketOrderQty"},
		{"price-step", func(o *fwtrading.Order, _ *bybit.InstrumentRules) { o.LimitPrice = "2.51" }, fwtrading.ErrRejected, "tickSize"},
		{"notional", func(o *fwtrading.Order, _ *bybit.InstrumentRules) { o.LimitPrice = "2.45" }, fwtrading.ErrRejected, "minOrderAmt"},
		{"unused-market-rules", func(_ *fwtrading.Order, r *bybit.InstrumentRules) {
			r.LotSizeFilter.MaxMarketOrderQty, r.LotSizeFilter.QuotePrecision = "broken", ""
		}, nil, ""},
		{"unused-limit-rules", func(o *fwtrading.Order, r *bybit.InstrumentRules) {
			o.LimitPrice = ""
			r.LotSizeFilter.MinOrderAmt, r.PriceFilter.TickSize = "broken", ""
			r.LotSizeFilter.MaxLimitOrderQty, r.LotSizeFilter.QuotePrecision = "", "broken"
		}, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			order, rules := bybitTestOrder(), bybitTestRules()
			order.LimitPrice = "2.50"
			tc.change(&order, &rules)
			err := checkBybitRules(order, rules)
			assertBybitError(t, err, tc.want)
			if tc.want == nil && err != nil {
				t.Fatal(err)
			}
			if tc.rule != "" && !strings.Contains(err.Error(), tc.rule) {
				t.Fatalf("missing rule %s in %v", tc.rule, err)
			}
		})
	}
	for _, field := range []string{"quantity", "price"} {
		for _, raw := range []string{"", "0", "-1", "+1", "1e1", "oops", " 1", strings.Repeat("1", 257)} {
			if field == "price" && raw == "" {
				continue
			}
			t.Run(field+"-"+raw, func(t *testing.T) {
				order := bybitTestOrder()
				order.LimitPrice = "2.50"
				if field == "quantity" {
					order.Quantity = raw
				} else {
					order.LimitPrice = raw
				}
				err := checkBybitRules(order, bybitTestRules())
				assertBybitError(t, err, fwtrading.ErrRejected)
				if !strings.Contains(err.Error(), field+" must be a positive plain decimal") {
					t.Fatalf("wrong refusal: %v", err)
				}
			})
		}
	}
	for _, field := range []string{"basePrecision", "maxLimitOrderQty", "maxMarketOrderQty", "tickSize", "minOrderAmt"} {
		for _, raw := range []string{"", "0", "-1", "+1", "1e1", "oops"} {
			t.Run(field+"-"+raw, func(t *testing.T) {
				order, rules := bybitTestOrder(), bybitTestRules()
				order.LimitPrice = "2.50"
				switch field {
				case "basePrecision":
					rules.LotSizeFilter.BasePrecision = raw
				case "maxLimitOrderQty":
					rules.LotSizeFilter.MaxLimitOrderQty = raw
				case "maxMarketOrderQty":
					order.LimitPrice, rules.LotSizeFilter.MaxMarketOrderQty = "", raw
				case "tickSize":
					rules.PriceFilter.TickSize = raw
				case "minOrderAmt":
					rules.LotSizeFilter.MinOrderAmt = raw
				}
				err := checkBybitRules(order, rules)
				assertBybitError(t, err, domain.ErrUpstream)
				if !strings.Contains(err.Error(), field) {
					t.Fatalf("missing rule %s: %v", field, err)
				}
			})
		}
	}
}

func TestBybitVenueStatus(t *testing.T) {
	for _, tc := range []struct {
		text string
		want fwtrading.VenueStatus
	}{
		{"New", fwtrading.VenueStatusOpen},
		{"PartiallyFilled", fwtrading.VenueStatusOpen},
		{"Filled", fwtrading.VenueStatusFilled},
		{"Cancelled", fwtrading.VenueStatusCancelled},
		{"PartiallyFilledCanceled", fwtrading.VenueStatusCancelled},
		{"Rejected", fwtrading.VenueStatusCancelled},
		{"Untriggered", ""}, {"Triggered", ""}, {"Deactivated", ""},
		{"unknown", ""}, {"", ""}, {"new", ""},
	} {
		t.Run(tc.text, func(t *testing.T) {
			got, known := bybitVenueStatus(tc.text)
			if got != tc.want || known != (tc.want != "") {
				t.Fatalf("status=(%s,%t), want %s", got, known, tc.want)
			}
		})
	}
}

func bybitTestExecution() bybitExecution {
	return bybitExecution{
		Symbol: "BTCUSDT", OrderID: "venue-1", OrderLinkID: "client-1",
		Side: "Buy", OrderQty: "3.000", LeavesQty: "2.00",
		ExecID: "exec-1", ExecPrice: "8.00", ExecQty: "1.000",
		ExecFee: "0.010", FeeCurrency: "BTC", ExecType: "Trade",
		ExecTime: "1700000000001",
	}
}

func TestBybitExecutionFill(t *testing.T) {
	for _, tc := range []struct {
		name, base, quote, fee string
		wantError              bool
	}{
		{"missing-base", "", "USDT", "0.01", true},
		{"missing-quote", "BTC", "", "0.01", true},
		{"empty-coins", "", "", "0.01", true},
		{"zero-without-coins", "", "", "0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			execution := bybitTestExecution()
			execution.ExecFee = tc.fee
			got, err := execution.fill(tc.base, tc.quote)
			if tc.wantError {
				assertBybitError(t, err, domain.ErrUpstream)
			} else if err != nil || got.fill.Commission != nil || got.fill.CommissionLeg != "" {
				t.Fatalf("fill=%+v err=%v", got, err)
			}
		})
	}
	for _, tc := range []struct {
		name, fee, currency, leaves string
		leg                         fwtrading.CommissionLeg
	}{
		{"base", "0.010", "BTC", "2.00", fwtrading.CommissionLegBase},
		{"quote", "0.010", "USDT", "2.00", fwtrading.CommissionLegQuote},
		{"other", "0.010", "MNT", "2.00", ""},
		{"zero", "0.000", "", "2.00", ""},
		{"zero-with-asset", "0", "BTC", "2.00", ""},
		{"final", "0", "", "0.00", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			execution := bybitTestExecution()
			execution.ExecFee, execution.FeeCurrency, execution.LeavesQty = tc.fee, tc.currency, tc.leaves
			want := fwtrading.Fill{
				Quantity: "1.000", Price: "8.00", CumQuantity: "1", LeavesQuantity: "2",
				At: time.UnixMilli(1700000000001).UTC(), CommissionLeg: tc.leg,
			}
			if tc.name == "final" {
				want.CumQuantity, want.LeavesQuantity, want.Final = "3", "0", true
			}
			if tc.fee == "0.010" {
				want.Commission = &domain.Commission{Amount: tc.fee, Currency: tc.currency}
			}
			got, err := execution.fill("BTC", "USDT")
			if err != nil || !reflect.DeepEqual(got.fill, want) {
				t.Fatalf("fill=%+v err=%v, want=%+v", got, err, want)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		change func(*bybitExecution)
	}{
		{"type", func(e *bybitExecution) { e.ExecType = "Funding" }},
		{"missing-type", func(e *bybitExecution) { e.ExecType = "" }},
		{"missing-id", func(e *bybitExecution) { e.ExecID = "" }},
		{"missing-fee-currency", func(e *bybitExecution) { e.FeeCurrency = "" }},
		{"negative-fee", func(e *bybitExecution) { e.ExecFee = "-0.01" }},
		{"leaves-exceed-order", func(e *bybitExecution) { e.LeavesQty = "4" }},
		{"execution-exceeds-cumulative", func(e *bybitExecution) { e.ExecQty = "1.001" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			execution := bybitTestExecution()
			tc.change(&execution)
			_, err := execution.fill("BTC", "USDT")
			assertBybitError(t, err, domain.ErrUpstream)
		})
	}
	for _, raw := range []string{"", "0", "-1", "+1", "1.1", "1e3", "oops", " 1", "9223372036854775808"} {
		t.Run("time-"+raw, func(t *testing.T) {
			execution := bybitTestExecution()
			execution.ExecTime = raw
			_, err := execution.fill("BTC", "USDT")
			assertBybitError(t, err, domain.ErrUpstream)
		})
	}
	for _, field := range []string{"execQty", "execPrice", "orderQty", "leavesQty", "execFee"} {
		for _, raw := range []string{"", "-1", "+1", "1e1", "oops", "0", strings.Repeat("1", 257)} {
			if raw == "0" && (field == "leavesQty" || field == "execFee") {
				continue
			}
			t.Run(field+"-"+raw, func(t *testing.T) {
				execution := bybitTestExecution()
				switch field {
				case "execQty":
					execution.ExecQty = raw
				case "execPrice":
					execution.ExecPrice = raw
				case "orderQty":
					execution.OrderQty = raw
				case "leavesQty":
					execution.LeavesQty = raw
				case "execFee":
					execution.ExecFee = raw
				}
				_, err := execution.fill("BTC", "USDT")
				assertBybitError(t, err, domain.ErrUpstream)
			})
		}
	}
}

func TestBybitExecutionExtraFees(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		valid bool
	}{
		{"", true}, {"null", true}, {`""`, true}, {"[]", true},
		{" \n [ ] \t", true}, {" null ", true},
		{`[{"feeType":"GST","fee":"0.1"}]`, false},
		{`"GST"`, false}, {`" "`, false}, {`"[]"`, false},
		{"{}", false}, {"0", false}, {"false", false}, {"[null]", false},
		{"[", false}, {" ", false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			execution := bybitTestExecution()
			execution.ExtraFees = json.RawMessage(tc.raw)
			_, err := execution.fill("BTC", "USDT")
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertBybitError(t, err, domain.ErrUpstream)
				if !errors.Is(err, errBybitExtraFees) {
					t.Fatalf("missing extra fees sentinel: %v", err)
				}
			}
		})
	}
	for _, rawFees := range []string{`""`, "[]"} {
		raw := `{"symbol":"BTCUSDT","orderId":"venue-1","orderLinkId":"client-1","side":"Buy","orderQty":"3.000","leavesQty":"2.00","execId":"exec-1","execPrice":"8.00","execQty":"1.000","execFee":"0.010","feeCurrency":"BTC","execType":"Trade","execTime":"1700000000001","extraFees":` + rawFees + `}`
		var got bybitExecution
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatal(err)
		}
		want := bybitTestExecution()
		want.ExtraFees = json.RawMessage(rawFees)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("decoded execution=%+v, want=%+v", got, want)
		}
		if _, err := got.fill("BTC", "USDT"); err != nil {
			t.Fatal(err)
		}
	}
}

func bybitTestVenueOrder() bybitOrder {
	return bybitOrder{
		OrderID: "venue-1", OrderLinkID: "client-1", Symbol: "BTCUSDT",
		Side: "Buy", OrderStatus: "PartiallyFilled", Qty: "3.000",
		LeavesQty: "2", CumExecQty: "1.000", RejectReason: "EC_NoError",
		CreatedTime: "1700000000000",
	}
}

func TestBybitOrderSnapshot(t *testing.T) {
	first := bybitTestExecution()
	second := first
	second.ExecID, second.ExecQty, second.LeavesQty = "exec-2", "2.000", "0"
	for _, tc := range []struct {
		name, status, cumulative, reason string
		executions                       []bybitExecution
		wantStatus                       fwtrading.VenueStatus
	}{
		{"new", "New", "0", "", nil, fwtrading.VenueStatusOpen},
		{"partial", "PartiallyFilled", "1.000", "EC_NoError", []bybitExecution{first}, fwtrading.VenueStatusOpen},
		{"filled", "Filled", "3.000", "", []bybitExecution{second, first, second, first}, fwtrading.VenueStatusFilled},
		{"partial-cancel", "PartiallyFilledCanceled", "1.000", "venue-reason", []bybitExecution{first, first}, fwtrading.VenueStatusCancelled},
		{"cancelled", "Cancelled", "0", "", nil, fwtrading.VenueStatusCancelled},
		{"rejected", "Rejected", "0", "venue-reason", nil, fwtrading.VenueStatusCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			order := bybitTestVenueOrder()
			order.OrderStatus, order.CumExecQty, order.RejectReason = tc.status, tc.cumulative, tc.reason
			got, err := order.snapshot(tc.executions, "BTC", "USDT")
			if err != nil {
				t.Fatal(err)
			}
			want := fwtrading.Snapshot{
				VenueOrderID: "venue-1", Status: tc.wantStatus, FilledQuantity: tc.cumulative, Reason: tc.reason,
			}
			if want.Reason == "EC_NoError" {
				want.Reason = ""
			}
			if len(tc.executions) != 0 {
				want.Fills = []fwtrading.Fill{{
					Quantity: "1.000", Price: "8.00", CumQuantity: "1", LeavesQuantity: "2",
					Commission:    &domain.Commission{Amount: "0.010", Currency: "BTC"},
					CommissionLeg: fwtrading.CommissionLegBase, At: time.UnixMilli(1700000000001).UTC(),
				}}
				if tc.status == "Filled" {
					fill := want.Fills[0]
					fill.Quantity, fill.CumQuantity, fill.LeavesQuantity, fill.Final = "2.000", "3", "0", true
					want.Fills = append(want.Fills, fill)
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("snapshot=%+v, want=%+v", got, want)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		change func(*bybitOrder, *[]bybitExecution)
	}{
		{"missing-id", func(o *bybitOrder, _ *[]bybitExecution) { o.OrderID = "" }},
		{"missing-symbol", func(o *bybitOrder, _ *[]bybitExecution) { o.Symbol = "" }},
		{"missing-side", func(o *bybitOrder, _ *[]bybitExecution) { o.Side = "" }},
		{"missing-symbol-without-executions", func(o *bybitOrder, es *[]bybitExecution) { o.Symbol, *es = "", nil }},
		{"missing-side-without-executions", func(o *bybitOrder, es *[]bybitExecution) { o.Side, *es = "", nil }},
		{"qty", func(o *bybitOrder, _ *[]bybitExecution) { o.Qty = "bad" }},
		{"zero-qty", func(o *bybitOrder, _ *[]bybitExecution) { o.Qty = "0" }},
		{"cum", func(o *bybitOrder, _ *[]bybitExecution) { o.CumExecQty = "bad" }},
		{"negative-cum", func(o *bybitOrder, _ *[]bybitExecution) { o.CumExecQty = "-1" }},
		{"overfilled", func(o *bybitOrder, _ *[]bybitExecution) { o.CumExecQty = "4" }},
		{"filled-incomplete", func(o *bybitOrder, _ *[]bybitExecution) { o.OrderStatus = "Filled" }},
		{"wrong-order", func(_ *bybitOrder, es *[]bybitExecution) { (*es)[0].OrderID = "other" }},
		{"wrong-symbol", func(_ *bybitOrder, es *[]bybitExecution) { (*es)[0].Symbol = "ETHUSDT" }},
		{"wrong-side", func(_ *bybitOrder, es *[]bybitExecution) { (*es)[0].Side = "Sell" }},
		{"missing-execution-symbol", func(_ *bybitOrder, es *[]bybitExecution) { (*es)[0].Symbol = "" }},
		{"missing-execution-side", func(_ *bybitOrder, es *[]bybitExecution) { (*es)[0].Side = "" }},
		{"wrong-order-qty", func(_ *bybitOrder, es *[]bybitExecution) { (*es)[0].OrderQty = "4" }},
		{"bad-order-qty", func(_ *bybitOrder, es *[]bybitExecution) { (*es)[0].OrderQty = "bad" }},
		{"conflicting-id", func(_ *bybitOrder, es *[]bybitExecution) { e := (*es)[0]; e.ExecPrice = "9"; *es = append(*es, e) }},
		{"bad-execution", func(_ *bybitOrder, es *[]bybitExecution) { (*es)[0].ExecFee = "bad" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			order, executions := bybitTestVenueOrder(), []bybitExecution{first}
			tc.change(&order, &executions)
			_, err := order.snapshot(executions, "BTC", "USDT")
			assertBybitError(t, err, domain.ErrUpstream)
		})
	}
	t.Run("rendered-cumulative", func(t *testing.T) {
		order := bybitTestVenueOrder()
		order.Qty = strings.Repeat("9", plainDecimalMaxLength)
		execution := bybitTestExecution()
		execution.OrderQty, execution.LeavesQty = order.Qty, "0.1"
		want, err := execution.fill("BTC", "USDT")
		if err != nil {
			t.Fatal(err)
		}
		got, err := order.snapshot([]bybitExecution{execution}, "BTC", "USDT")
		if err != nil || !reflect.DeepEqual(got.Fills, []fwtrading.Fill{want.fill}) {
			t.Fatalf("fills=%+v err=%v, want=%+v", got.Fills, err, want)
		}
	})
	for _, status := range []string{"Untriggered", "Triggered", "Deactivated", "unknown", ""} {
		order := bybitTestVenueOrder()
		order.OrderStatus = status
		_, err := order.snapshot([]bybitExecution{first}, "BTC", "USDT")
		assertBybitError(t, err, domain.ErrUpstream)
		if !errors.Is(err, errBybitOrderStatus) {
			t.Fatalf("missing status sentinel: %v", err)
		}
	}
	var decoded bybitOrder
	raw := `{"orderId":"venue-1","orderLinkId":"client-1","symbol":"BTCUSDT","side":"Buy","orderStatus":"PartiallyFilled","qty":"3.000","leavesQty":"2","cumExecQty":"1.000","rejectReason":"EC_NoError","createdTime":"1700000000000"}`
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != bybitTestVenueOrder() {
		t.Fatalf("order=%+v", decoded)
	}
}

func TestBybitRandomizedFillAssembly(t *testing.T) {
	for seed := int64(1); seed <= 128; seed++ {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			count := 2 + rng.Intn(30)
			parts := make([]int64, count)
			var total int64
			for i := range parts {
				parts[i] = 1 + int64(rng.Intn(1000))
				total += parts[i]
			}
			qty := decimal.New(total, -3)
			order := bybitTestVenueOrder()
			order.Qty, order.CumExecQty, order.LeavesQty, order.OrderStatus = qty.String(), qty.String(), "0", "Filled"
			canonical := make([]bybitExecution, count)
			want := fwtrading.Snapshot{VenueOrderID: "venue-1", Status: fwtrading.VenueStatusFilled, FilledQuantity: qty.String()}
			var cumulative int64
			for i, part := range parts {
				cumulative += part
				e := bybitTestExecution()
				e.OrderQty, e.ExecQty, e.LeavesQty = qty.String(), decimal.New(part, -3).String(), decimal.New(total-cumulative, -3).String()
				e.ExecID, e.ExecTime = fmt.Sprintf("exec-%d", i), fmt.Sprint(1700000000001+i/2)
				e.ExecPrice, e.ExecFee = decimal.New(int64(1+rng.Intn(10000)), -2).String(), "0.001"
				canonical[i] = e
				want.Fills = append(want.Fills, fwtrading.Fill{
					Quantity: e.ExecQty, Price: e.ExecPrice, CumQuantity: decimal.New(cumulative, -3).String(),
					LeavesQuantity: e.LeavesQty, Final: i == count-1,
					Commission: &domain.Commission{Amount: "0.001", Currency: "BTC"}, CommissionLeg: fwtrading.CommissionLegBase,
					At: time.UnixMilli(1700000000001 + int64(i/2)).UTC(),
				})
			}
			view := append([]bybitExecution(nil), canonical...)
			for i := 0; i < count; i++ {
				view = append(view, canonical[rng.Intn(count)])
			}
			rng.Shuffle(len(view), func(i, j int) { view[i], view[j] = view[j], view[i] })
			var pages [][]bybitExecution
			for start := 0; start < len(view); {
				end := min(len(view), start+2+rng.Intn(5))
				pages = append(pages, append([]bybitExecution(nil), view[start:end]...))
				if end == len(view) {
					break
				}
				start = end - 1
			}
			var collected []bybitExecution
			for _, page := range pages {
				collected = append(collected, page...)
			}
			got, err := order.snapshot(collected, "BTC", "USDT")
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("snapshot=%+v err=%v, want=%+v", got, err, want)
			}
			conflict := canonical[rng.Intn(count)]
			conflict.ExecQty = decimal.RequireFromString(conflict.ExecQty).Add(decimal.New(1, -3)).String()
			collected = append(collected, conflict)
			_, err = order.snapshot(collected, "BTC", "USDT")
			assertBybitError(t, err, domain.ErrUpstream)
		})
	}
}
