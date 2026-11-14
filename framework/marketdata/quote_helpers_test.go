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

package marketdata_test

import (
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/marketdata"
)

func TestQuoteStale(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.November, 11, 12, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name string
		age  time.Duration
		want bool
	}{
		{name: "TTL-1ns", age: marketdata.FreshnessTTL - time.Nanosecond, want: false},
		{name: "TTL", age: marketdata.FreshnessTTL, want: true},
		{name: "TTL+1ns", age: marketdata.FreshnessTTL + time.Nanosecond, want: true},
		{name: "future AsOf", age: -time.Nanosecond, want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := marketdata.QuoteStale(now.Add(-tt.age), now); got != tt.want {
				t.Fatalf("QuoteStale(age=%s) = %t, want %t", tt.age, got, tt.want)
			}
		})
	}
}

func TestInverseQuoteSwapsSidesAndPreservesSource(t *testing.T) {
	t.Parallel()
	asOf := time.Date(2026, time.November, 11, 12, 0, 0, 0, time.UTC)
	quote := domain.MarketDataQuote{
		AsOf:           asOf,
		ReceivedAt:     asOf.Add(time.Second),
		Instance:       domain.ExternalID("quote-source"),
		ExternalSymbol: "EUR.USD",
		BaseAsset:      "EUR",
		QuoteAsset:     "USD",
		Mark:           "2",
		Bid:            "4",
		Ask:            "8",
	}
	instrument := domain.MarketDataInstrument{
		BaseAsset: "EUR", QuoteAsset: "USD",
	}
	got, ok := marketdata.InverseQuote(quote, instrument)
	if !ok {
		t.Fatal("InverseQuote ok = false, want true")
	}
	want := domain.MarketDataQuote{
		AsOf:           quote.AsOf,
		ReceivedAt:     quote.ReceivedAt,
		Instance:       quote.Instance,
		ExternalSymbol: quote.ExternalSymbol,
		BaseAsset:      instrument.QuoteAsset,
		QuoteAsset:     instrument.BaseAsset,
		Mark:           "0.5",
		Bid:            "0.125",
		Ask:            "0.25",
	}
	if got != want {
		t.Fatalf("InverseQuote = %+v, want %+v", got, want)
	}
}

func TestInverseQuoteUninvertible(t *testing.T) {
	t.Parallel()
	quote := domain.MarketDataQuote{Mark: "invalid", Bid: "0", Ask: "-2"}
	if got, ok := marketdata.InverseQuote(quote, domain.MarketDataInstrument{}); ok {
		t.Fatalf("InverseQuote = %+v, ok = true, want false", got)
	}
}
