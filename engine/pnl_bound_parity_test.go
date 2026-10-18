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

package engine

import (
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/domain"
)

// FuzzPnlBoundValidateMatchesEngineParse holds, for every writing
// domain.ParseEngineDecimal accepts, that a global Spot Funds P&L-bounds
// barrier in USD carrying it as its only bound, lower or upper, passes
// Validate exactly when the engine parses the bound: pnlBoundOptions, which
// maps the bounds onto the engine with param.NewPnlFromString, takes it. Its
// seeds are the writings
// TestLimitSpotFundsPnlBounds_ValidateTakesEngineWritingsOnly in
// framework/domain expects Validate to accept or refuse by a reading of
// rust_decimal's source, plus a few plain ones, so go test without -fuzz holds
// those expectations against the engine's own parser.
func FuzzPnlBoundValidateMatchesEngineParse(f *testing.F) {
	// The largest digits an engine decimal holds, 2^96 - 1.
	const maxDigits = "79228162514264337593543950335"
	for _, writing := range []string{
		"-100", "100.5", "-0.0001", "+5", ".5", "5.", "-0",
		"0." + strings.Repeat("0", 27) + "1",
		maxDigits, maxDigits[:1] + "." + maxDigits[1:],
		"-1e2", "1E2", "1.5e-3", ".-5",
		"1." + strings.Repeat("0", 29),
		"0." + strings.Repeat("0", 29),
		"10." + strings.Repeat("0", 28),
		maxDigits + ".0",
		"0", "1", "-1", "0.1", "12345.6789", "-" + maxDigits,
	} {
		f.Add(writing)
	}
	f.Fuzz(func(t *testing.T, writing string) {
		if _, err := domain.ParseEngineDecimal(writing); err != nil {
			t.Skip("not a decimal ParseEngineDecimal accepts")
		}
		for _, bound := range []struct{ name, lower, upper string }{
			{"lower", writing, ""},
			{"upper", "", writing},
		} {
			validateErr := (domain.LimitSpotFundsPnlBounds{
				Scope: domain.ScopeGlobal, Currency: "USD",
				LowerBound: bound.lower, UpperBound: bound.upper,
			}).Validate()
			_, _, parseErr := pnlBoundOptions(
				bound.lower, bound.upper, "spot_funds_pnl_bounds",
			)
			if (validateErr == nil) != (parseErr == nil) {
				t.Fatalf(
					"%s bound %q: Validate = %v, while the engine's parse = %v",
					bound.name, writing, validateErr, parseErr,
				)
			}
		}
	})
}
