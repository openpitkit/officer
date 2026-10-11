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

func TestToolError(t *testing.T) {
	candidates := &backend.DestinationAmbiguousError{Candidates: []backend.TradingDestinationStatus{
		{Connection: domain.ExternalID("first"), Label: "First paper"},
		{Connection: domain.ExternalID("second"), Label: "Second paper"},
	}}
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"live", fmt.Errorf("refused: %w", domain.ErrLiveTradingUnavailable), "live_trading_unavailable: live trading unavailable"},
		{"managed", fmt.Errorf("refused: %w", domain.ErrVenueOrderManaged), "venue_order_managed: the order is managed by its trading venue"},
		{"asset", fmt.Errorf("refused: %w", domain.NewAssetMissingError("USDT")), `asset_missing: refused: asset "USDT" does not exist`},
		{"validation", fmt.Errorf("limit price: %w", domain.ErrInvalid), "validation: limit price: invalid"},
		{"not-found", fmt.Errorf("order: %w", domain.ErrNotFound), "not_found: order: not found"},
		{"candidates", fmt.Errorf("choose: %w", candidates), "destination_ambiguous: several destinations; name one; first (First paper); second (Second paper)"},
		{"bare-ambiguous", fmt.Errorf("choose: %w", domain.ErrDestinationAmbiguous), "destination_ambiguous: several destinations; name one"},
		{"candidates-only-when-ambiguous", fmt.Errorf("%w: %w", domain.ErrInvalid, candidates), "validation: invalid: several destinations; name one"},
		{"uncovered", errors.New("offline"), "internal: internal error"},
		{"upstream", fmt.Errorf("alpaca: HTTP 401: %w", domain.ErrUpstream), "upstream: The provider couldn't complete the request. Check your provider access and the request, then try again."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolError(tc.err).Error(); got != tc.want {
				t.Fatalf("error = %q, want %q", got, tc.want)
			}
		})
	}
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
