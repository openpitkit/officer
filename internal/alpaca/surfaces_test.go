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

package alpaca_test

import (
	"errors"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/internal/marketdata"
	"go.openpit.dev/officer/internal/trading"
)

// TestCredentialsAgreeAcrossSurfaces builds the market-data and the trading
// connector from the same credentials JSON: both accept it, or both refuse it
// with a domain validation error of the same constraint.
func TestCredentialsAgreeAcrossSurfaces(t *testing.T) {
	for _, tc := range []struct {
		name     string
		raw      string
		accepted bool
	}{
		{"valid", `{"apiKey":"test-key","apiSecret":"test-secret"}`, true},
		{"padded", `{"apiKey":"  test-key ","apiSecret":" test-secret  "}`, true},
		{"legacy key and secret", `{"key":"test-key","secret":"test-secret"}`, false},
		{"missing apiSecret", `{"apiKey":"test-key"}`, false},
		{"blank apiKey", `{"apiKey":"  ","apiSecret":"test-secret"}`, false},
		{"extra member", `{"apiKey":"test-key","apiSecret":"test-secret","extra":"value"}`, false},
		{"non-string member", `{"apiKey":"test-key","apiSecret":7}`, false},
		{"array", `["test-key","test-secret"]`, false},
		{"invalid JSON", `{"apiKey":"test-key",`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			md, mdErr := marketdata.NewAlpacaConnector(domain.MarketDataInstance{Credentials: tc.raw})
			if mdErr == nil {
				md.Close()
			}
			tr, trErr := trading.NewAlpacaConnector(domain.TradingConnection{
				Mode: domain.TradingModePaper, Credentials: tc.raw,
			})
			if trErr == nil {
				tr.Close()
			}
			if (mdErr == nil) != tc.accepted || (trErr == nil) != tc.accepted {
				t.Fatalf("market data err = %v, trading err = %v, want accepted %t", mdErr, trErr, tc.accepted)
			}
			if tc.accepted {
				return
			}
			if !errors.Is(mdErr, domain.ErrInvalid) || !errors.Is(trErr, domain.ErrInvalid) {
				t.Fatalf("market data err = %v, trading err = %v, want domain.ErrInvalid from both", mdErr, trErr)
			}
			constraint := domain.ValidationConstraint(mdErr)
			if constraint == "" || domain.ValidationConstraint(trErr) != constraint {
				t.Fatalf("constraints = %q (market data), %q (trading), want one non-empty constraint",
					constraint, domain.ValidationConstraint(trErr))
			}
		})
	}
}
