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
// Please see https://openpit.dev and the OWNERS file for details.

package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestTradingModeSupported(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		mode TradingMode
		want bool
	}{
		{"paper", TradingModePaper, true},
		{"live", TradingModeLive, true},
		{"unknown", "unknown", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := TradingModeSupported(test.mode); got != test.want {
				t.Fatalf("TradingModeSupported() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestValidateTradingConnection(t *testing.T) {
	t.Parallel()
	valid := TradingConnection{
		Provider: TradingProviderAlpaca, Label: "Alpaca paper", VenueIdentity: "paper-account",
		Mode: TradingModePaper, Credentials: `{"key":"value"}`,
	}
	if err := ValidateTradingConnection(valid); err != nil {
		t.Fatalf("valid connection: %v", err)
	}
	withFeeAccount := valid
	withFeeAccount.FeeAccount = "fees"
	if err := ValidateTradingConnection(withFeeAccount); err != nil {
		t.Fatalf("valid fee account: %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*TradingConnection)
	}{
		{"provider leading space", func(v *TradingConnection) { v.Provider = " alpaca" }},
		{"fee account whitespace", func(v *TradingConnection) { v.FeeAccount = " fees" }},
		{"fee account path segment", func(v *TradingConnection) { v.FeeAccount = ".." }},
		{"label trailing space", func(v *TradingConnection) { v.Label = "label " }},
		{"mode whitespace", func(v *TradingConnection) { v.Mode = " paper" }},
		{"credentials whitespace", func(v *TradingConnection) { v.Credentials = " {}" }},
		{"empty provider", func(v *TradingConnection) { v.Provider = "" }},
		{"empty label", func(v *TradingConnection) { v.Label = "" }},
		{"unsupported mode", func(v *TradingConnection) { v.Mode = "unknown" }},
		{"empty credentials", func(v *TradingConnection) { v.Credentials = "" }},
		{"invalid credentials", func(v *TradingConnection) { v.Credentials = "{" }},
		{"array credentials", func(v *TradingConnection) { v.Credentials = "[]" }},
		{"string credentials", func(v *TradingConnection) { v.Credentials = `"secret"` }},
		{"number credentials", func(v *TradingConnection) { v.Credentials = "1" }},
		{"null credentials", func(v *TradingConnection) { v.Credentials = "null" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			value := valid
			test.mutate(&value)
			assertTradingInvalid(t, ValidateTradingConnection(value))
		})
	}
}

func TestValidateTradingConnectionSettings(t *testing.T) {
	t.Parallel()
	if err := ValidateTradingConnectionSettings("paper", `{"key":"value"}`); err != nil {
		t.Fatalf("valid settings: %v", err)
	}
	for _, test := range []struct {
		name, label, credentials string
	}{
		{"empty label", "", "{}"},
		{"label leading space", " paper", "{}"},
		{"label trailing space", "paper ", "{}"},
		{"credentials leading space", "paper", " {}"},
		{"credentials trailing space", "paper", "{} "},
		{"empty credentials", "paper", ""},
		{"invalid credentials", "paper", "{"},
		{"array credentials", "paper", "[]"},
		{"string credentials", "paper", `"secret"`},
		{"number credentials", "paper", "1"},
		{"boolean credentials", "paper", "true"},
		{"null credentials", "paper", "null"},
		{"trailing JSON", "paper", "{} {}"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assertTradingInvalid(t, ValidateTradingConnectionSettings(test.label, test.credentials))
		})
	}
}

func TestTradingModeVocabulary(t *testing.T) {
	for _, mode := range []TradingMode{"paper", "live"} {
		if !TradingModeSupported(mode) {
			t.Fatalf("mode %q is not supported", mode)
		}
	}
	if TradingModePaper != "paper" || TradingModeLive != "live" {
		t.Fatal("mode constants do not use paper/live wire values")
	}
	for _, mode := range []TradingMode{"", "test", "real", "unknown", "Paper", " live"} {
		if TradingModeSupported(mode) {
			t.Fatalf("invalid mode %q is supported", mode)
		}
	}
}

func TestTradingConnectionVenueIdentity(t *testing.T) {
	connection := TradingConnection{
		Provider: TradingProviderAlpaca, Label: "paper", Mode: TradingModePaper,
		Credentials: `{}`, VenueIdentity: "venue-account",
	}
	if err := ValidateTradingConnection(connection); err != nil {
		t.Fatalf("canonical identity rejected: %v", err)
	}
	for _, identity := range []string{"", " ", " account", "account ", "\taccount\n"} {
		connection.VenueIdentity = identity
		if err := ValidateTradingConnection(connection); !errors.Is(err, ErrInvalid) {
			t.Fatalf("identity %q: error = %v, want ErrInvalid", identity, err)
		}
	}
}

func TestValidateTradingConnectionDoesNotExposeCredentials(t *testing.T) {
	t.Parallel()
	const sensitiveMarker = "do-not-expose"
	err := ValidateTradingConnection(TradingConnection{
		Provider:      TradingProviderAlpaca,
		VenueIdentity: "Alpaca",
		Label:         "Alpaca",
		Mode:          TradingModePaper,
		Credentials:   `{"private_key":"` + sensitiveMarker + `"`,
	})
	assertTradingInvalid(t, err)
	if strings.Contains(err.Error(), sensitiveMarker) {
		t.Fatal("credential validation error exposed credential input")
	}
}

func TestValidateTradingInstrument(t *testing.T) {
	t.Parallel()
	valid := TradingInstrument{
		Connection: "connection", ExternalSymbol: "AAPL",
		BaseAsset: "AAPL", QuoteAsset: "USD",
	}
	if err := ValidateTradingInstrument(valid); err != nil {
		t.Fatalf("valid instrument: %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*TradingInstrument)
	}{
		{"zero connection", func(v *TradingInstrument) { v.Connection = "" }},
		{"empty symbol", func(v *TradingInstrument) { v.ExternalSymbol = "" }},
		{"symbol whitespace", func(v *TradingInstrument) { v.ExternalSymbol = " AAPL" }},
		{"base whitespace", func(v *TradingInstrument) { v.BaseAsset = "AAPL " }},
		{"quote whitespace", func(v *TradingInstrument) { v.QuoteAsset = " USD" }},
		{"invalid base", func(v *TradingInstrument) { v.BaseAsset = "" }},
		{"invalid quote", func(v *TradingInstrument) { v.QuoteAsset = "" }},
		{"same assets", func(v *TradingInstrument) { v.QuoteAsset = "AAPL" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			value := valid
			test.mutate(&value)
			assertTradingInvalid(t, ValidateTradingInstrument(value))
		})
	}
}

func TestValidateTradingAccess(t *testing.T) {
	t.Parallel()
	valid := TradingAccess{Account: "acc-1", Connection: "connection"}
	if err := ValidateTradingAccess(valid); err != nil {
		t.Fatalf("valid access: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*TradingAccess)
	}{
		{"invalid account", func(v *TradingAccess) { v.Account = " account" }},
		{"zero connection", func(v *TradingAccess) { v.Connection = "" }},
		{"venue account whitespace", func(v *TradingAccess) { v.VenueAccount = " venue" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			value := valid
			test.mutate(&value)
			assertTradingInvalid(t, ValidateTradingAccess(value))
		})
	}
}

func TestValidateTradingRoute(t *testing.T) {
	t.Parallel()
	for _, route := range []string{`{}`, `{"time_in_force":"day"}`, `{ "nested": [1, null] }`} {
		if err := ValidateTradingRoute(route); err != nil {
			t.Fatalf("valid route %q: %v", route, err)
		}
	}
	for _, route := range []string{"", " {}", "{} ", "{", "[]", `"day"`, "1", "null"} {
		t.Run(route, func(t *testing.T) {
			t.Parallel()
			assertTradingInvalid(t, ValidateTradingRoute(route))
		})
	}
}

func TestValidateTradingDestination(t *testing.T) {
	t.Parallel()
	valid := TradingDestination{Connection: "connection", Route: `{}`}
	if err := ValidateTradingDestination(valid); err != nil {
		t.Fatalf("valid destination: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*TradingDestination)
	}{
		{"zero connection", func(v *TradingDestination) { v.Connection = "" }},
		{"venue account whitespace", func(v *TradingDestination) { v.VenueAccount = "venue " }},
		{"route whitespace", func(v *TradingDestination) { v.Route = " {}" }},
		{"empty route", func(v *TradingDestination) { v.Route = "" }},
		{"invalid route", func(v *TradingDestination) { v.Route = "{" }},
		{"array route", func(v *TradingDestination) { v.Route = "[]" }},
		{"string route", func(v *TradingDestination) { v.Route = `"day"` }},
		{"number route", func(v *TradingDestination) { v.Route = "1" }},
		{"null route", func(v *TradingDestination) { v.Route = "null" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			value := valid
			test.mutate(&value)
			assertTradingInvalid(t, ValidateTradingDestination(value))
		})
	}
}

func TestValidateVenueOrder(t *testing.T) {
	t.Parallel()
	valid := VenueOrder{
		Order: "order", Connection: "connection", Route: `{}`,
		ClientOrderID: "client-order",
	}
	if err := ValidateVenueOrder(valid); err != nil {
		t.Fatalf("valid venue order: %v", err)
	}
	acknowledged := valid
	acknowledged.SendAttempted = true
	acknowledged.VenueOrderID = "venue-order"
	if err := ValidateVenueOrder(acknowledged); err != nil {
		t.Fatalf("valid acknowledged venue order: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*VenueOrder)
	}{
		{"zero order", func(v *VenueOrder) { v.Order = "" }},
		{"zero connection", func(v *VenueOrder) { v.Connection = "" }},
		{"venue account whitespace", func(v *VenueOrder) { v.VenueAccount = " venue" }},
		{"route whitespace", func(v *VenueOrder) { v.Route = "{} " }},
		{"client id whitespace", func(v *VenueOrder) { v.ClientOrderID = " client" }},
		{"venue id whitespace", func(v *VenueOrder) { v.VenueOrderID = " venue-order" }},
		{"acknowledged without send attempt", func(v *VenueOrder) {
			v.VenueOrderID = "venue-order"
		}},
		{"empty client id", func(v *VenueOrder) { v.ClientOrderID = "" }},
		{"empty route", func(v *VenueOrder) { v.Route = "" }},
		{"invalid route", func(v *VenueOrder) { v.Route = "{" }},
		{"array route", func(v *VenueOrder) { v.Route = "[]" }},
		{"string route", func(v *VenueOrder) { v.Route = `"day"` }},
		{"number route", func(v *VenueOrder) { v.Route = "1" }},
		{"null route", func(v *VenueOrder) { v.Route = "null" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			value := valid
			test.mutate(&value)
			assertTradingInvalid(t, ValidateVenueOrder(value))
		})
	}
}

func assertTradingInvalid(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error does not wrap ErrInvalid: %v", err)
	}
}
