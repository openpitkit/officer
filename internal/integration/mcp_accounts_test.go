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

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	"go.openpit.dev/officer/framework/node"
)

// accountsSource records the account calls the account tools make and answers
// them with stored and the recorder's err.
type accountsSource struct {
	fakeSource
	sourceRecorder
	stored domain.Account
}

func (s *accountsSource) CreateAccount(_ context.Context, account domain.Account) (domain.Account, error) {
	return s.stored, s.record("create", account)
}

func (s *accountsSource) UpdateAccount(_ context.Context, oldID domain.AccountID, account domain.Account) (domain.Account, error) {
	return s.stored, s.record("update", oldID, account)
}

func (s *accountsSource) DeleteAccount(_ context.Context, id domain.AccountID, force bool) error {
	return s.record("delete", id, force)
}

func (s *accountsSource) SetAccountCurrency(_ context.Context, id domain.AccountID, currency string) error {
	return s.record("currency", id, currency)
}

// GetAccountState answers the read-back that follows a write with stored, or
// with accStateErr, and records it as a read of id, keeping the write's
// arguments.
func (s *accountsSource) GetAccountState(_ context.Context, id domain.AccountID) (domain.Account, node.AccountLimits, error) {
	s.calls = append(s.calls, fmt.Sprintf("read %q", id))
	if s.accStateErr != nil {
		return domain.Account{}, node.AccountLimits{}, s.accStateErr
	}
	return s.stored, node.AccountLimits{}, nil
}

// accountsFixture answers with a stored account that differs from every input
// the tests send and belongs to a blocked group, so a result shows the stored
// row with its group's block joined in.
func accountsFixture() *accountsSource {
	src := &accountsSource{stored: domain.Account{
		Code: "desk-2", Title: "Desk two", Currency: "USD",
		EffectiveCurrency: "USD", CurrencyOrigin: "account",
		GroupCurrency: "EUR", DefaultCurrency: "USD", GroupCode: "desks",
	}}
	src.groups = []domain.AccountGroup{{Code: "desks", Blocked: true, BlockReason: "desk halt"}}
	return src
}

// storedAccountBody is the REST account body of accountsFixture's stored row.
const storedAccountBody = `{"account":{"code":"desk-2","title":"Desk two","pnl":"",` +
	`"pnlHaltReason":"","group":"desks","currency":"USD","effectiveCurrency":"USD",` +
	`"currencyOrigin":"account","currencyCascade":{"account":"USD","group":"EUR",` +
	`"default":"USD"},"notes":"","positionCount":0,"blockReason":"desk halt",` +
	`"blockSource":"group","accountBlockReason":"","groupBlockReason":"desk halt",` +
	`"blocked":true,"accountBlocked":false,"groupBlocked":true}}`

// accountToolInputs holds one valid input per account tool.
var accountToolInputs = map[string]map[string]any{
	"create_account": {"code": "desk-1"},
	"update_account": {"account": "desk-1", "code": "desk-2", "title": ""},
	"delete_account": {"account": "desk-1"},
}

// accountCurrencyInputs holds a valid set_account_currency input. It is kept
// apart from accountToolInputs because the core refuses the currency change
// otherwise than it refuses a change of the account set.
var accountCurrencyInputs = map[string]map[string]any{
	"set_account_currency": {"account": "desk-1", "currency": "EUR"},
}

// accountsRecorded is accountsFixture as the shared tool drivers take it.
func accountsRecorded() (frameworkmcp.Source, *fakeSource, *sourceRecorder) {
	src := accountsFixture()
	return src, &src.fakeSource, &src.sourceRecorder
}

// TestAccountToolsSuccess covers each account tool's success: its arguments
// reach the Source verbatim, spaces included, and the result is the REST body,
// which a currency change reads back under the account it named, as REST does.
func TestAccountToolsSuccess(t *testing.T) {
	t.Parallel()
	runToolSuccess(t, accountsRecorded, []toolSuccessCase{
		{"create_account", map[string]any{"code": " desk-1 ", "title": " Desk one ", "currency": " USD "},
			[]string{"create"}, []any{domain.Account{Code: " desk-1 ", Title: " Desk one ", Currency: " USD "}},
			"account desk-2 created", storedAccountBody},
		{"create_account", map[string]any{"code": "desk-1"},
			[]string{"create"}, []any{domain.Account{Code: "desk-1"}},
			"account desk-2 created", storedAccountBody},
		{"update_account", map[string]any{"account": " desk-1 ", "code": " desk-2 ", "title": " "},
			[]string{"update"}, []any{domain.AccountID(" desk-1 "), domain.Account{Code: " desk-2 ", Title: " "}},
			"account desk-2 updated", storedAccountBody},
		{"delete_account", map[string]any{"account": " desk-1 ", "force": true},
			[]string{"delete"}, []any{domain.AccountID(" desk-1 "), true},
			"account  desk-1  deleted", `{"account":" desk-1 ","force":true}`},
		{"delete_account", map[string]any{"account": "desk-1"},
			[]string{"delete"}, []any{domain.AccountID("desk-1"), false},
			"account desk-1 deleted", `{"account":"desk-1"}`},
		{"set_account_currency", map[string]any{"account": " desk-1 ", "currency": " EUR "},
			[]string{"currency", `read " desk-1 "`}, []any{domain.AccountID(" desk-1 "), " EUR "},
			"account desk-2 currency set", storedAccountBody},
		{"set_account_currency", map[string]any{"account": "desk-1", "currency": ""},
			[]string{"currency", `read "desk-1"`}, []any{domain.AccountID("desk-1"), ""},
			"account desk-2 currency cleared", storedAccountBody},
	})
}

// TestAccountToolsSourceRefusal covers a Source refusal of each account tool:
// it reaches the agent under the code REST answers with the Source's message,
// the dependents blocking a delete listed in it.
func TestAccountToolsSourceRefusal(t *testing.T) {
	t.Parallel()
	runToolRefusals(t, accountsRecorded, accountToolInputs, []toolRefusal{
		{"validation", fmt.Errorf("account code: %w", domain.ErrInvalid),
			"validation: account code: invalid"},
		accountsNotManagedRefusal,
		{"conflict", fmt.Errorf("account %q: %w", "desk-2", domain.ErrAlreadyExists),
			`conflict: account "desk-2": already exists`},
		{"not_found", fmt.Errorf("account %q: %w", "desk-1", domain.ErrNotFound),
			`not_found: account "desk-1": not found`},
		{"has_dependents", fmt.Errorf("account %q: %w", "desk-1", domain.NewHasDependentsError(
			[]domain.DependentCount{{Kind: "balance", Count: 2}, {Kind: "trade", Count: 1}},
		)), "has_dependents: has dependents; balance (2); trade (1)"},
	})
	for _, name := range []string{"create_account", "update_account"} {
		t.Run(name+"/group-read", func(t *testing.T) {
			src := accountsFixture()
			src.listGroupsErr = errors.New("store down")
			res := callTool[map[string]any](t, src, name, accountToolInputs[name])
			requireToolError(t, res.IsError)
			if got := textContent(res.Content); got != "internal: internal error" {
				t.Fatalf("group read failure = %q, want internal: internal error", got)
			}
		})
	}
	// A currency change is refused as invalid input or a missing account, never
	// as a change of the account set; the currency guard's refusals are pinned
	// against the core in TestSetAccountCurrencyOnControlPlane.
	runToolRefusals(t, accountsRecorded, accountCurrencyInputs, []toolRefusal{
		{"validation", fmt.Errorf("asset code: %w", domain.ErrInvalid),
			"validation: asset code: invalid"},
		{"not_found", fmt.Errorf("account %q: %w", "desk-1", domain.ErrNotFound),
			`not_found: account "desk-1": not found`},
	})
	// The reads that build the result after the change fail as REST's do.
	for _, tc := range []struct {
		name string
		fail func(*accountsSource)
		want string
	}{
		{"read-back", func(src *accountsSource) {
			src.accStateErr = fmt.Errorf("account %q: %w", "desk-1", domain.ErrNotFound)
		}, `not_found: account "desk-1": not found`},
		{"group-read", func(src *accountsSource) {
			src.listGroupsErr = errors.New("store down")
		}, "internal: internal error"},
	} {
		t.Run("set_account_currency/"+tc.name, func(t *testing.T) {
			src := accountsFixture()
			tc.fail(src)
			res := callTool[map[string]any](t, src, "set_account_currency",
				accountCurrencyInputs["set_account_currency"])
			requireToolError(t, res.IsError)
			if got := textContent(res.Content); got != tc.want {
				t.Fatalf("%s failure = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// TestAccountToolsDisabled covers each account tool with its command disabled:
// the agent receives the panel notice and the Source is not called.
func TestAccountToolsDisabled(t *testing.T) {
	t.Parallel()
	runToolsDisabled(t, accountsRecorded, accountToolInputs)
	runToolsDisabled(t, accountsRecorded, accountCurrencyInputs)
}
