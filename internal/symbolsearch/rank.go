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

// Package symbolsearch ranks symbols from a catalogue independently of
// connector result types.
package symbolsearch

import (
	"sort"
	"strings"
)

const limit = 50

type scoredSymbol struct {
	symbol string
	score  int
}

// Rank returns up to 50 matching catalogue keys, best matches first, breaking
// ties by symbol. Empty queries return no matches. Catalogue values are
// ignored.
func Rank[Value any](known map[string]Value, query string) []string {
	q := strings.TrimSpace(query)
	if q == "" {
		return []string{}
	}
	normQuery := NormalizedKey(q)
	if normQuery == "" {
		return []string{}
	}
	base, quote, hasPair := SplitPair(q)
	scored := make([]scoredSymbol, 0)
	for symbol := range known {
		score := symbolScore(symbol, q, normQuery, base, quote, hasPair)
		if score == 0 {
			continue
		}
		scored = append(scored, scoredSymbol{symbol: symbol, score: score})
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			return scored[i].symbol < scored[j].symbol
		}
		return scored[i].score > scored[j].score
	})
	if len(scored) > limit {
		scored = scored[:limit]
	}
	symbols := make([]string, 0, len(scored))
	for _, item := range scored {
		symbols = append(symbols, item.symbol)
	}
	return symbols
}

func symbolScore(
	symbol, query, normQuery, base, quote string, hasPair bool,
) int {
	trimmed := strings.TrimSpace(symbol)
	if trimmed == "" {
		return 0
	}
	if trimmed == query {
		return 1000
	}
	if strings.EqualFold(trimmed, query) {
		return 950
	}
	normSymbol := NormalizedKey(trimmed)
	if normSymbol == "" {
		return 0
	}
	if hasPair && normSymbol == base+quote {
		return 900
	}
	if normSymbol == normQuery {
		return 850
	}
	if strings.HasPrefix(normSymbol, normQuery) {
		return 650
	}
	if strings.Contains(normSymbol, normQuery) {
		return 400
	}
	upperQuery := strings.ToUpper(query)
	upperSymbol := strings.ToUpper(trimmed)
	if strings.Contains(upperSymbol, upperQuery) {
		return 350
	}
	return 0
}

// NormalizedKey folds case and removes pair separators and whitespace.
func NormalizedKey(value string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(value)) {
		switch r {
		case '/', '\\', '_', '-', ' ', '\t', '\n', '\r':
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// SplitPair returns normalized base and quote keys when value has two parts
// separated by pair separators or whitespace.
func SplitPair(value string) (base, quote string, ok bool) {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		switch r {
		case '/', '\\', '_', '-', ' ', '\t', '\n', '\r':
			return true
		default:
			return false
		}
	})
	if len(parts) != 2 {
		return "", "", false
	}
	base = NormalizedKey(parts[0])
	quote = NormalizedKey(parts[1])
	if base == "" || quote == "" {
		return "", "", false
	}
	return base, quote, true
}
