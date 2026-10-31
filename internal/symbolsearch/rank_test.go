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

package symbolsearch_test

import (
	"fmt"
	"reflect"
	"testing"

	"go.openpit.dev/officer/internal/symbolsearch"
)

func TestSplitPair(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		base  string
		quote string
		ok    bool
	}{
		{"slash", "eth/usdt", "ETH", "USDT", true},
		{"backslash", "eth\\usdt", "ETH", "USDT", true},
		{"underscore", "eth_usdt", "ETH", "USDT", true},
		{"hyphen", "eth-usdt", "ETH", "USDT", true},
		{"space", "eth usdt", "ETH", "USDT", true},
		{"tab", "eth\tusdt", "ETH", "USDT", true},
		{"newline", "eth\nusdt", "ETH", "USDT", true},
		{"carriage_return", "eth\rusdt", "ETH", "USDT", true},
		{"surrounding_space", " \teth/usdt\r\n", "ETH", "USDT", true},
		{"reverse_pair", "usdt/eth", "USDT", "ETH", true},
		{"mixed_case", "eTh/UsDt", "ETH", "USDT", true},
		{"three_parts", "ETH/USDT/BTC", "", "", false},
		{"empty_base", "/USDT", "", "", false},
		{"whitespace_base", "\v/USDT", "", "", false},
		{"empty_quote", "ETH/", "", "", false},
		{"whitespace_quote", "ETH/\v/", "", "", false},
		{"empty", "", "", "", false},
		{"separators_only", " /_-\\\t\n", "", "", false},
		{"unseparated", "ETHUSDT", "", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			base, quote, ok := symbolsearch.SplitPair(test.value)
			if base != test.base || quote != test.quote || ok != test.ok {
				t.Fatalf(
					"SplitPair(%q) = (%q, %q, %t); want (%q, %q, %t)",
					test.value, base, quote, ok, test.base, test.quote, test.ok,
				)
			}
		})
	}
}

func TestNormalizedKey(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  string
	}{
		{"case_fold", "eThUsDt", "ETHUSDT"},
		{"slash", "eth/usdt", "ETHUSDT"},
		{"backslash", "eth\\usdt", "ETHUSDT"},
		{"underscore", "eth_usdt", "ETHUSDT"},
		{"hyphen", "eth-usdt", "ETHUSDT"},
		{"space", "eth usdt", "ETHUSDT"},
		{"tab", "eth\tusdt", "ETHUSDT"},
		{"newline", "eth\nusdt", "ETHUSDT"},
		{"carriage_return", "eth\rusdt", "ETHUSDT"},
		{"surrounding_space", " \tethusdt\r\n", "ETHUSDT"},
		{"surrounding_vertical_tab", "\vethusdt\v", "ETHUSDT"},
		{"mixed_separators", " e/t\\h_u-s d\tt\n\r", "ETHUSDT"},
		{"empty", "", ""},
		{"separators_only", " /_-\\\t\n", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := symbolsearch.NormalizedKey(test.value); got != test.want {
				t.Fatalf(
					"NormalizedKey(%q) = %q; want %q",
					test.value, got, test.want,
				)
			}
		})
	}
}

func TestRank(t *testing.T) {
	for _, test := range []struct {
		name    string
		query   string
		symbols []string
		want    []string
	}{
		{
			name:  "exact",
			query: "ethusdt",
			symbols: []string{
				"ETHUSDT", "ethusdt", "ETHUSDT-PERP", "WETHUSDT", "BTCEUR",
			},
			want: []string{"ethusdt", "ETHUSDT", "ETHUSDT-PERP", "WETHUSDT"},
		},
		{
			name:    "case_fold",
			query:   "eThUsDt",
			symbols: []string{"ETH/USDT", "ETHUSDT"},
			want:    []string{"ETHUSDT", "ETH/USDT"},
		},
		{
			name:    "slash_pair",
			query:   "ETH/USDT",
			symbols: []string{"ETHUSDT-PERP", "ETHUSDT", "BTCEUR"},
			want:    []string{"ETHUSDT", "ETHUSDT-PERP"},
		},
		{
			name:    "hyphen_pair",
			query:   "eth-usdt",
			symbols: []string{"ETHUSDT-PERP", "ETHUSDT", "BTCEUR"},
			want:    []string{"ETHUSDT", "ETHUSDT-PERP"},
		},
		{
			name:    "normalized",
			query:   "ETHUSDT",
			symbols: []string{"ETHUSDT0", "ETH_USDT"},
			want:    []string{"ETH_USDT", "ETHUSDT0"},
		},
		{
			name:    "prefix",
			query:   "ETH",
			symbols: []string{"AETHUSDT", "ETHUSDT", "ETHBTC", "BTCEUR"},
			want:    []string{"ETHBTC", "ETHUSDT", "AETHUSDT"},
		},
		{
			name:    "contains",
			query:   "THUS",
			symbols: []string{"WETHUSDT", "ETH/USDT", "BTCEUR"},
			want:    []string{"ETH/USDT", "WETHUSDT"},
		},
		{
			name:    "empty",
			query:   "",
			symbols: []string{"ETHUSDT"},
			want:    []string{},
		},
		{
			name:    "separators",
			query:   " /_-\\ \t\n",
			symbols: []string{"ETHUSDT"},
			want:    []string{},
		},
		{
			name:    "ties",
			query:   "ETH",
			symbols: []string{"ETHUSDT", "ETHUSD", "ETHBTC"},
			want:    []string{"ETHBTC", "ETHUSD", "ETHUSDT"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			known := make(map[string]int, len(test.symbols))
			for index, symbol := range test.symbols {
				known[symbol] = index
			}
			got := symbolsearch.Rank(known, test.query)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Rank(%q) = %#v; want %#v", test.query, got, test.want)
			}
		})
	}
}

func TestRankLimit(t *testing.T) {
	known := make(map[string]struct{}, 55)
	want := make([]string, 0, 50)
	for index := 54; index >= 0; index-- {
		known[fmt.Sprintf("ETH%02d", index)] = struct{}{}
	}
	for index := 0; index < 50; index++ {
		want = append(want, fmt.Sprintf("ETH%02d", index))
	}
	got := symbolsearch.Rank(known, "ETH")
	if len(got) != 50 {
		t.Fatalf("Rank returned %d symbols; want 50", len(got))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Rank returned %v; want first 50 symbols in order: %v", got, want)
	}
}
