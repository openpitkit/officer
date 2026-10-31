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
	"reflect"
	"strings"
	"testing"

	"github.com/scmhub/ibapi"
	"go.openpit.dev/officer/framework/domain"
	fwtrading "go.openpit.dev/officer/framework/trading"
)

func TestIBPreCheck(t *testing.T) {
	for _, tc := range []struct {
		name, sec string
		change    func(*fwtrading.Order)
		count     int
		want      error
		text      string
	}{
		{name: "stock", sec: "STK", count: 1},
		{name: "class share", sec: "STK", count: 1, change: func(o *fwtrading.Order) { o.Symbol = "BRK B" }},
		{name: "leading space", count: 1, change: func(o *fwtrading.Order) { o.Symbol = " BRK" }, want: fwtrading.ErrUnsupported},
		{name: "double space", count: 1, change: func(o *fwtrading.Order) { o.Symbol = "BRK  B" }, want: fwtrading.ErrUnsupported},
		{name: "three parts", count: 1, change: func(o *fwtrading.Order) { o.Symbol = "BRK B C" }, want: fwtrading.ErrUnsupported},
		{name: "FX", sec: "CASH", count: 1, change: func(o *fwtrading.Order) { o.Symbol, o.BaseAsset = "EUR.USD", "eur" }},
		{name: "crypto limit", sec: "CRYPTO", count: 1, change: func(o *fwtrading.Order) { o.Symbol, o.BaseAsset, o.LimitPrice = "BTC.USD", "BTC", "100" }},
		{name: "crypto quantity market buy", sec: "CRYPTO", count: 1, change: func(o *fwtrading.Order) { o.Symbol, o.BaseAsset = "BTC.USD", "BTC" }, want: fwtrading.ErrUnsupported},
		{name: "volume crypto market buy", sec: "CRYPTO", count: 1, change: func(o *fwtrading.Order) {
			o.Symbol, o.BaseAsset, o.AmountKind = "BTC.USD", "BTC", domain.OrderAmountKindVolume
		}, want: fwtrading.ErrUnsupported},
		{name: "volume", sec: "STK", count: 1, change: func(o *fwtrading.Order) { o.AmountKind = domain.OrderAmountKindVolume }, want: fwtrading.ErrUnsupported},
		{name: "no matches", sec: "STK", count: 0, want: fwtrading.ErrRejected, text: "0 matches"},
		{name: "several matches", sec: "CASH", count: 2, change: func(o *fwtrading.Order) { o.Symbol, o.BaseAsset = "EUR.USD", "EUR" }, want: fwtrading.ErrRejected, text: "2 matches"},
		{name: "base mismatch", sec: "CASH", count: 1, change: func(o *fwtrading.Order) { o.Symbol = "EUR.USD" }, want: fwtrading.ErrUnsupported},
		{name: "quote mismatch", sec: "CASH", count: 1, change: func(o *fwtrading.Order) { o.Symbol, o.BaseAsset, o.QuoteAsset = "EUR.USD", "EUR", "EUR" }, want: fwtrading.ErrUnsupported},
		{name: "derivative shape", count: 1, change: func(o *fwtrading.Order) { o.Symbol = "AAPL 202701 C 100" }, want: fwtrading.ErrUnsupported},
		{name: "extra dots", count: 1, change: func(o *fwtrading.Order) { o.Symbol = "A.B.C" }, want: fwtrading.ErrUnsupported},
		{name: "missing assets", sec: "STK", count: 1, change: func(o *fwtrading.Order) { o.QuoteAsset = "" }, want: fwtrading.ErrUnsupported},
		{name: "unmanaged", sec: "STK", count: 1, change: func(o *fwtrading.Order) { o.VenueAccount = "DU-other" }, want: fwtrading.ErrRejected},
		{name: "bad route", sec: "STK", count: 1, change: func(o *fwtrading.Order) { o.Route = `{"timeInForce":"opg"}` }, want: fwtrading.ErrRejected},
		{name: "missing route", sec: "STK", count: 1, change: func(o *fwtrading.Order) { o.Route = `{}` }, want: fwtrading.ErrRejected},
		{name: "unknown route", sec: "STK", count: 1, change: func(o *fwtrading.Order) { o.Route = `{"timeInForce":"day","x":"y"}` }, want: fwtrading.ErrRejected},
		{name: "zero quantity", sec: "STK", count: 1, change: func(o *fwtrading.Order) { o.Quantity = "0" }, want: fwtrading.ErrRejected},
		{name: "bad price", sec: "STK", count: 1, change: func(o *fwtrading.Order) { o.LimitPrice = "-1" }, want: fwtrading.ErrRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newFakeIBVenue()
			v.details = func(contract *ibapi.Contract) []*ibapi.ContractDetails {
				if contract.SecType != tc.sec {
					return nil
				}
				if tc.sec == "STK" && (contract.Exchange != "SMART" || contract.Currency != "USD") {
					t.Errorf("stock lookup = %+v", contract)
				}
				if tc.sec == "CASH" && contract.Exchange != "IDEALPRO" || tc.sec == "CRYPTO" && contract.Exchange != "PAXOS" {
					t.Errorf("pair lookup = %+v", contract)
				}
				var result []*ibapi.ContractDetails
				for i := 0; i < tc.count; i++ {
					result = append(result, &ibapi.ContractDetails{Contract: *contract, OrderTypes: "MKT,LMT"})
				}
				return result
			}
			c := testIBConnector(t, v)
			subscribeIB(t, c)
			order := ibTestOrder()
			if tc.change != nil {
				tc.change(&order)
			}
			err := c.PreCheck(context.Background(), order)
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("precheck=%v want=%v", err, tc.want)
			}
			if tc.text != "" && !strings.Contains(err.Error(), tc.text) {
				t.Fatalf("missing match count: %v", err)
			}
			if v.places != 0 {
				t.Fatal("pre-check reached PlaceOrder")
			}
		})
	}
}

func TestIBSymbolProbes(t *testing.T) {
	v := newFakeIBVenue()
	v.details = func(contract *ibapi.Contract) []*ibapi.ContractDetails {
		if contract.SecType != "STK" {
			return nil
		}
		if contract.Currency != "" {
			t.Errorf("verification supplied currency: %s", contract.Currency)
		}
		var result []*ibapi.ContractDetails
		for i, listing := range []struct{ currency, exchange string }{{"USD", "NASDAQ"}, {"EUR", "IBIS"}} {
			copy := *contract
			copy.ConID = int64(i + 1)
			copy.Currency, copy.PrimaryExchange = listing.currency, listing.exchange
			result = append(result, &ibapi.ContractDetails{Contract: copy, OrderTypes: "MKT,LMT"})
		}
		return result
	}
	c := testIBConnector(t, v)
	verification, err := c.VerifySymbol(context.Background(), "AAPL")
	if err != nil || !verification.Exists || !verification.Tradable || !strings.Contains(verification.Details, "USD NASDAQ") || !strings.Contains(verification.Details, "EUR IBIS") {
		t.Fatalf("verification=%+v: %v", verification, err)
	}
	matches, err := c.SearchSymbols(context.Background(), "AAPL")
	want := []fwtrading.SymbolMatch{
		{Symbol: "AAPL", Class: "STK", Exchange: "NASDAQ", Tradable: true},
		{Symbol: "AAPL", Class: "STK", Exchange: "IBIS", Tradable: true},
	}
	if err != nil || !reflect.DeepEqual(matches, want) {
		t.Fatalf("stock search=%+v want=%+v: %v", matches, want, err)
	}
	before := v.connects
	matches, err = c.SearchSymbols(context.Background(), "  ")
	if err != nil || matches == nil || len(matches) != 0 || v.connects != before {
		t.Fatal("empty search contacted venue or returned nil")
	}
	for _, count := range []int{0, 1, 2} {
		v.details = func(contract *ibapi.Contract) []*ibapi.ContractDetails {
			if contract.SecType != "CRYPTO" {
				return nil
			}
			var result []*ibapi.ContractDetails
			for i := 0; i < count; i++ {
				result = append(result, &ibapi.ContractDetails{Contract: *contract, OrderTypes: "LMT"})
			}
			return result
		}
		verification, err = c.VerifySymbol(context.Background(), "BTC.USD")
		if err != nil || verification.Exists != (count == 1) {
			t.Fatalf("pair count=%d: %+v %v", count, verification, err)
		}
		if count != 1 && !strings.Contains(verification.Details, "matches") {
			t.Fatal("missing pair ambiguity detail")
		}
	}
	matches, err = c.SearchSymbols(context.Background(), "BTC.USD")
	want = []fwtrading.SymbolMatch{{Symbol: "BTC.USD", Class: "CRYPTO", Exchange: "PAXOS", BaseAsset: "BTC", QuoteAsset: "USD", Tradable: true}}
	if err != nil || !reflect.DeepEqual(matches, want) {
		t.Fatalf("search=%+v %v", matches, err)
	}
	for _, tc := range []struct {
		query, sec string
		want       fwtrading.SymbolMatch
	}{
		{"EUR.USD", "CASH", fwtrading.SymbolMatch{Symbol: "EUR.USD", Class: "CASH", Exchange: "IDEALPRO", BaseAsset: "EUR", QuoteAsset: "USD", Tradable: true}},
		{"AAPL", "STK", fwtrading.SymbolMatch{Symbol: "AAPL", Class: "STK", Tradable: true}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			v.details = func(contract *ibapi.Contract) []*ibapi.ContractDetails {
				if contract.SecType != tc.sec {
					return nil
				}
				return []*ibapi.ContractDetails{{Contract: *contract, OrderTypes: "MKT,LMT"}}
			}
			matches, err := c.SearchSymbols(context.Background(), tc.query)
			if err != nil || !reflect.DeepEqual(matches, []fwtrading.SymbolMatch{tc.want}) {
				t.Fatalf("search=%+v want=%+v: %v", matches, tc.want, err)
			}
		})
	}
}
