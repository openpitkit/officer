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

package backend

import (
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
)

// TestEveryAccountIsIdentity covers every method of the default scope, for a
// client and for the system caller: each returns exactly what the caller sent
// and refuses nobody, so lists stay unrestricted and the account set open.
func TestEveryAccountIsIdentity(t *testing.T) {
	t.Parallel()

	scope := EveryAccount{}
	if got := scope.Features(); got != (AccountFeatures{}) || !got.Accounts() {
		t.Fatalf("Features() = %+v, want accounts offered and no fixed account", got)
	}
	cases := []struct {
		name string
		call func(domain.Caller) (any, error)
		want any
	}{
		{
			name: "Account/omitted",
			call: func(c domain.Caller) (any, error) { return scope.Account(c, "") },
			want: domain.AccountID(""),
		},
		{
			name: "Account/named",
			call: func(c domain.Caller) (any, error) { return scope.Account(c, "acc-1") },
			want: domain.AccountID("acc-1"),
		},
		{
			name: "MissingAccount/omitted",
			call: func(c domain.Caller) (any, error) { return scope.MissingAccount(c, "") },
			want: domain.MissingAccountPolicy(""),
		},
		{
			name: "MissingAccount/create",
			call: func(c domain.Caller) (any, error) {
				return scope.MissingAccount(c, domain.MissingAccountCreate)
			},
			want: domain.MissingAccountCreate,
		},
		{
			name: "MissingAccount/reject",
			call: func(c domain.Caller) (any, error) {
				return scope.MissingAccount(c, domain.MissingAccountReject)
			},
			want: domain.MissingAccountReject,
		},
		{
			name: "List/omitted",
			call: func(c domain.Caller) (any, error) { return scope.List(c, "") },
			want: domain.AccountRestriction{},
		},
		{
			name: "List/named",
			call: func(c domain.Caller) (any, error) { return scope.List(c, "acc-1") },
			want: domain.AccountRestriction{},
		},
		{
			name: "Visible/unowned",
			call: func(c domain.Caller) (any, error) { return nil, scope.Visible(c, "") },
		},
		{
			name: "Visible/owned",
			call: func(c domain.Caller) (any, error) { return nil, scope.Visible(c, "acc-1") },
		},
		{
			name: "Group/omitted",
			call: func(c domain.Caller) (any, error) { return nil, scope.Group(c, "") },
		},
		{
			name: "Group/named",
			call: func(c domain.Caller) (any, error) { return nil, scope.Group(c, "desk") },
		},
		{
			name: "ManageAccounts",
			call: func(c domain.Caller) (any, error) { return nil, scope.ManageAccounts(c) },
		},
	}
	callers := map[string]domain.Caller{
		"client": {Source: domain.SourceAPI, Principal: "operator"},
		"system": auth.SystemCaller(),
	}
	for callerName, caller := range callers {
		for _, tc := range cases {
			t.Run(callerName+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				got, err := tc.call(caller)
				if err != nil || got != tc.want {
					t.Fatalf("= %#v, %v; want %#v, nil", got, err, tc.want)
				}
			})
		}
	}
}

// TestAccountFeaturesAccountFor covers both compositions: a named account is
// kept, and an omitted one becomes the fixed account, which is empty when
// clients see accounts.
func TestAccountFeaturesAccountFor(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		features  AccountFeatures
		requested domain.AccountID
		want      domain.AccountID
	}{
		{"accounts/omitted", AccountFeatures{}, "", ""},
		{"accounts/named", AccountFeatures{}, "acc-1", "acc-1"},
		{"one account/omitted", AccountFeatures{Account: "acc-1"}, "", "acc-1"},
		{"one account/named", AccountFeatures{Account: "acc-1"}, "acc-2", "acc-2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.features.AccountFor(tc.requested); got != tc.want {
				t.Fatalf("AccountFor(%q) = %q, want %q", tc.requested, got, tc.want)
			}
		})
	}
}

// fixedAccountScope is EveryAccount offering clients one fixed account.
type fixedAccountScope struct {
	EveryAccount
	account domain.AccountID
}

func (s fixedAccountScope) Features() AccountFeatures {
	return AccountFeatures{Account: s.account}
}

// TestNewInstallsAccountScope covers the composition seam: without an option
// New installs EveryAccount, WithAccountScope installs the given scope, and an
// explicit nil scope is a construction error rather than a silent default.
func TestNewInstallsAccountScope(t *testing.T) {
	t.Parallel()

	svc, err := New(&limitTestNode{}, nil, nil, testLockSettlementPrice)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := svc.AccountFeatures(); got != (AccountFeatures{}) {
		t.Fatalf("default AccountFeatures() = %+v, want EveryAccount's", got)
	}

	svc, err = New(
		&limitTestNode{}, nil, nil, testLockSettlementPrice,
		WithAccountScope(fixedAccountScope{account: "acc-1"}),
	)
	if err != nil {
		t.Fatalf("New with scope: %v", err)
	}
	if got := svc.AccountFeatures(); got != (AccountFeatures{Account: "acc-1"}) || got.Accounts() {
		t.Fatalf("AccountFeatures() = %+v, want the installed scope's", got)
	}

	svc, err = New(
		&limitTestNode{}, nil, nil, testLockSettlementPrice, WithAccountScope(nil),
	)
	if err == nil || svc != nil {
		t.Fatalf("New(nil scope) returned service=%t err=%v, want an error", svc != nil, err)
	}
	if !strings.Contains(err.Error(), "account scope") {
		t.Fatalf("New(nil scope) error = %v, want it to name the account scope", err)
	}
}
