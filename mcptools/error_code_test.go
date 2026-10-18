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
	"net/http/httptest"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

// TestToolErrorMatchesREST proves that every domain sentinel and typed domain
// error carries the same code and message over MCP as over REST.
func TestToolErrorMatchesREST(t *testing.T) {
	wrapped := func(err error) error { return fmt.Errorf("context: %w", err) }
	blocked := func(scope domain.LimitScope) error {
		return domain.NewCurrencyChangeBlockedError(scope, "acc-1",
			fmt.Errorf("balances are not empty: %w", domain.ErrConflict))
	}
	cases := []struct {
		name string
		err  error
		code string
		// suffix is the text MCP appends to the REST message.
		suffix string
	}{
		{"ErrAccountMissing", wrapped(domain.ErrAccountMissing), "account_missing", ""},
		{"ErrAccountsNotManaged", wrapped(domain.ErrAccountsNotManaged), "accounts_not_managed", ""},
		{"ErrAlreadyExists", wrapped(domain.ErrAlreadyExists), "conflict", ""},
		{"ErrApprovalRequired", wrapped(domain.ErrApprovalRequired), "approval_required", ""},
		{"ErrAssetMissing", wrapped(domain.ErrAssetMissing), "internal", ""},
		{"ErrConflict", wrapped(domain.ErrConflict), "conflict", ""},
		{"ErrCurrencyValuedLimit", wrapped(domain.ErrCurrencyValuedLimit), "internal", ""},
		{"ErrDestinationAmbiguous", wrapped(domain.ErrDestinationAmbiguous), "destination_ambiguous", ""},
		{"ErrEngineRestarting", wrapped(domain.ErrEngineRestarting), "engine_restarting", ""},
		{"ErrExecutionReportRequired", wrapped(domain.ErrExecutionReportRequired), "execution_report_required", ""},
		{"ErrFeeAccountRequired", wrapped(domain.ErrFeeAccountRequired), "fee_account_required", ""},
		{"ErrForbidden", wrapped(domain.ErrForbidden), "forbidden", ""},
		{"ErrHasDependents", wrapped(domain.ErrHasDependents), "has_dependents", ""},
		{"ErrInvalid", wrapped(domain.ErrInvalid), "validation", ""},
		{"ErrLiveTradingUnavailable", wrapped(domain.ErrLiveTradingUnavailable), "live_trading_unavailable", ""},
		{"ErrLoosening", wrapped(domain.ErrLoosening), "loosening_refused", ""},
		{"ErrMasterKeyRequired", wrapped(domain.ErrMasterKeyRequired), "master_key_required", ""},
		{"ErrNoChange", wrapped(domain.ErrNoChange), "internal", ""},
		{"ErrNoDestination", wrapped(domain.ErrNoDestination), "no_destination", ""},
		{"ErrNotFound", wrapped(domain.ErrNotFound), "not_found", ""},
		{"ErrNotImplemented", wrapped(domain.ErrNotImplemented), "not_implemented", ""},
		{"ErrReservedGroup", wrapped(domain.ErrReservedGroup), "forbidden", ""},
		{"ErrTerminalOrder", wrapped(domain.ErrTerminalOrder), "terminal_order", ""},
		{"ErrTooLarge", wrapped(domain.ErrTooLarge), "too_large", ""},
		{"ErrTradingModeMismatch", wrapped(domain.ErrTradingModeMismatch), "trading_mode_mismatch", ""},
		{"ErrUpstream", wrapped(domain.ErrUpstream), "upstream", ""},
		{"ErrVenueOrderManaged", wrapped(domain.ErrVenueOrderManaged), "venue_order_managed", ""},
		{"AccountMissingError", wrapped(domain.NewAccountMissingError("acc-1")), "account_missing", ""},
		{"AssetMissingError", wrapped(domain.NewAssetMissingError("USDT")), "asset_missing", ""},
		{"CurrencyChangeBlockedError/account", wrapped(blocked(domain.ScopeAccount)), "currency_change_blocked", ""},
		{"CurrencyChangeBlockedError/account_group", wrapped(blocked(domain.ScopeAccountGroup)), "currency_change_blocked", ""},
		{"CurrencyChangeBlockedError/global", wrapped(blocked(domain.ScopeGlobal)), "conflict", ""},
		{"HasDependentsError", wrapped(domain.NewHasDependentsError([]domain.DependentCount{{Kind: "orders", Count: 2}})), "has_dependents", "; orders (2)"},
		{"DestinationAmbiguousError", wrapped(&backend.DestinationAmbiguousError{Candidates: []backend.TradingDestinationStatus{{Connection: domain.ExternalID("conn"), Label: "Paper"}}}), "destination_ambiguous", "; conn (Paper)"},
		{"ValidationError", wrapped(domain.NewValidationError("/price", "format", "price must be a decimal")), "validation", ""},
		{"account-missing-over-not-found", fmt.Errorf("%w: %w", domain.NewAccountMissingError("acc-2"), domain.ErrNotFound), "account_missing", ""},
		{"managed-over-conflict", fmt.Errorf("context: %w: %w", domain.ErrConflict, domain.ErrVenueOrderManaged), "venue_order_managed", ""},
		{"uncovered", errors.New("store down"), "internal", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restCode, restMessage := restError(t, tc.err)
			text := toolError(tc.err).Error()
			mcpCode, mcpMessage, ok := strings.Cut(text, ": ")
			if !ok {
				t.Fatalf("MCP text %q carries no code", text)
			}
			if restCode != tc.code || mcpCode != tc.code {
				t.Fatalf("code: REST %q, MCP %q, want %q", restCode, mcpCode, tc.code)
			}
			if mcpMessage != restMessage+tc.suffix {
				t.Fatalf("message: MCP %q, REST %q", mcpMessage, restMessage)
			}
		})
	}
}

// restError answers err over REST and returns the code and message the client
// reads: errors[0].code and detail of a problem body, else the error member.
func restError(t *testing.T, err error) (string, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	httpx.WriteErr(rec, err)
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Detail string `json:"detail"`
		Errors []struct {
			Code string `json:"code"`
		} `json:"errors"`
	}
	if decodeErr := json.Unmarshal(rec.Body.Bytes(), &body); decodeErr != nil {
		t.Fatalf("decode %s: %v", rec.Body, decodeErr)
	}
	if rec.Header().Get("Content-Type") == "application/problem+json" {
		if len(body.Errors) != 1 {
			t.Fatalf("problem errors = %+v", body.Errors)
		}
		return body.Errors[0].Code, body.Detail
	}
	return body.Error.Code, body.Error.Message
}
