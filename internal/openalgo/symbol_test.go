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

package openalgo

import "testing"

func TestParseSpotSymbol(t *testing.T) {
	for _, tc := range []struct {
		external, exchange, symbol string
	}{
		{"NSE:RELIANCE", "NSE", "RELIANCE"},
		{"BSE:INFY", "BSE", "INFY"},
		{"MCX:GOLD", "", ""},
		{"NFO:OPTION", "", ""},
		{"nse:abc", "", ""},
		{"NSE:A:B", "NSE", "A:B"},
		{"", "", ""}, {"RELIANCE", "", ""},
		{":RELIANCE", "", ""}, {"NSE:", "", ""},
		{" NSE:A", "", ""}, {"NSE :A", "", ""},
		{"NSE: A", "", ""}, {"NSE:A\t", "", ""},
	} {
		t.Run(tc.external, func(t *testing.T) {
			instrument, err := ParseSpotSymbol(tc.external)
			if (err != nil) != (tc.exchange == "") || instrument != (Instrument{Exchange: tc.exchange, Symbol: tc.symbol}) {
				t.Fatal("symbol split or validation changed")
			}
			if err != nil && err.Error() != SymbolForm {
				t.Fatal("invalid symbol did not explain its form")
			}
		})
	}
}

func TestOpenAlgoInstrumentString(t *testing.T) {
	for _, tc := range []struct {
		instrument Instrument
		external   string
	}{
		{Instrument{Exchange: "NSE", Symbol: "RELIANCE"}, "NSE:RELIANCE"},
		{Instrument{Exchange: "BSE", Symbol: "INFY"}, "BSE:INFY"},
		{Instrument{Exchange: "NSE", Symbol: "A:B"}, "NSE:A:B"},
	} {
		t.Run(tc.external, func(t *testing.T) {
			if got := tc.instrument.String(); got != tc.external {
				t.Fatalf("instrument string = %q, want %q", got, tc.external)
			}
		})
	}
}
