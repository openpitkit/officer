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

package mcptools

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
)

func TestTradingToolError(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		prefix string
	}{
		{"live", domain.ErrLiveTradingUnavailable, "live_trading_unavailable: "},
		{"mode", domain.ErrTradingModeMismatch, "trading_mode_mismatch: "},
		{"fee", domain.ErrFeeAccountRequired, "fee_account_required: "},
		{"managed", domain.ErrVenueOrderManaged, "venue_order_managed: "},
		{"none", domain.ErrNoDestination, "no_destination: "},
		{"ambiguous", domain.ErrDestinationAmbiguous, "destination_ambiguous: "},
		{"asset", domain.NewAssetMissingError("USDT"), "asset_missing: "},
		{"ordinary", errors.New("offline"), "send failed: "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wrapped := fmt.Errorf("refused: %w", tc.err)
			if got := tradingToolError("send", wrapped, nil).Error(); got != tc.prefix+wrapped.Error() {
				t.Fatalf("error: %q", got)
			}
		})
	}
	t.Run("candidates", func(t *testing.T) {
		err := fmt.Errorf("choose: %w", &backend.DestinationAmbiguousError{Candidates: []backend.TradingDestinationStatus{
			{Connection: domain.ExternalID("first"), Label: "First paper"},
			{Connection: domain.ExternalID("second"), Label: "Second paper"},
		}})
		got := tradingToolError("send", err, nil).Error()
		if !strings.HasPrefix(got, "destination_ambiguous: "+err.Error()) || !strings.Contains(got, "first (First paper)") || !strings.Contains(got, "second (Second paper)") {
			t.Fatalf("candidate refusal: %q", got)
		}
	})
	t.Run("redaction-preserves-code", func(t *testing.T) {
		err := fmt.Errorf("live_trading_unavailable: %w", domain.ErrLiveTradingUnavailable)
		redact := tradingCredentialRedactor(map[string]any{"key": "live_trading_unavailable"})
		got := tradingToolError("create", err, redact).Error()
		if got != "live_trading_unavailable: [REDACTED]: live trading unavailable" {
			t.Fatalf("redacted prefix: %q", got)
		}
	})
	t.Run("redaction-skips-blank-values", func(t *testing.T) {
		err := errors.New("alpaca: apiSecret is required")
		redact := tradingCredentialRedactor(map[string]any{"apiKey": "key-value", "apiSecret": " "})
		got := tradingToolError("create trading connection", err, redact).Error()
		if got != "create trading connection failed: alpaca: apiSecret is required" {
			t.Fatalf("blank credential redaction: %q", got)
		}
	})
}

func TestUpdateTradingConnectionInputObjects(t *testing.T) {
	for i, raw := range []string{`{"connection":"conn","credentials":null}`, `{"connection":"conn","credentials":[]}`, `{"connection":"conn","credentials":"secret-value"}`} {
		t.Run(fmt.Sprintf("invalid-%d", i), func(t *testing.T) {
			var input updateTradingConnectionInput
			if err := json.Unmarshal([]byte(raw), &input); err == nil || err.Error() != "credentials must be a JSON object" {
				t.Fatalf("non-object credentials refusal: %v", err)
			}
		})
	}
	for i, raw := range []string{`{"connection":"conn"}`, `{"connection":"conn","credentials":{}}`} {
		t.Run(fmt.Sprintf("valid-%d", i), func(t *testing.T) {
			var input updateTradingConnectionInput
			if err := json.Unmarshal([]byte(raw), &input); err != nil {
				t.Fatal(err)
			}
			if input.Connection != "conn" || input.Label != nil {
				t.Fatal("input not preserved")
			}
			if strings.Contains(raw, "credentials") != (input.Credentials != nil) {
				t.Fatal("omitted and empty credentials collapsed")
			}
		})
	}
	t.Run("null-label-is-present", func(t *testing.T) {
		var input updateTradingConnectionInput
		if err := json.Unmarshal([]byte(`{"connection":"conn","label":null}`), &input); err != nil {
			t.Fatal(err)
		}
		if input.Label == nil || *input.Label != "" {
			t.Fatal("null label treated as omitted")
		}
	})
}
