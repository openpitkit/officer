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

package backend

import (
	"context"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

// AccountScope is how the control plane treats the accounts a request names
// and the accounts whose data it returns. A composition installs one at build
// time with WithAccountScope; the default, EveryAccount, keeps every account
// reachable exactly as before. Every method except Features takes the caller
// (features are composition-wide, not per caller): venue processing runs as a
// system caller (domain.SourceSystem) and a scope must let it act on any
// account.
type AccountScope interface {
	// Features reports which account surfaces this composition offers clients.
	Features() AccountFeatures
	// Account resolves the account an operation acts on from the code the
	// caller named ("" when it named none).
	Account(caller domain.Caller, requested domain.AccountID) (domain.AccountID, error)
	// MissingAccount resolves the missing-account policy for that account from
	// the policy the caller sent ("" when it sent none).
	MissingAccount(caller domain.Caller, requested domain.MissingAccountPolicy) (domain.MissingAccountPolicy, error)
	// List returns the mandatory account restriction for a list the caller
	// reads; requested is the exact account code the caller named for the
	// list ("" when none). The zero restriction restricts nothing.
	List(caller domain.Caller, requested domain.AccountID) (domain.AccountRestriction, error)
	// Visible reports whether the caller may see or act on a resource owned by
	// owner ("" for a resource with no account). A refusal must not name the
	// owner account, and an implementation should wrap domain.ErrNotFound so a
	// hidden resource is reported like an absent one.
	Visible(caller domain.Caller, owner domain.AccountID) error
	// Group checks an account-group code the caller named ("" when none).
	Group(caller domain.Caller, requested string) error
	// ManageAccounts returns nil when the caller may change the account set
	// itself (accounts and groups), or the error a refused change carries.
	ManageAccounts(caller domain.Caller) error
}

// AccountFeatures describes the account surfaces offered to clients.
type AccountFeatures struct {
	// Account is the one account every client request acts on; empty means
	// clients see and choose every account and group.
	Account domain.AccountID
}

// Accounts is the "clients see accounts" feature flag: true when clients see
// and choose accounts and groups, that is, when no one account is fixed.
func (f AccountFeatures) Accounts() bool { return f.Account == "" }

// AccountFor is the account a client request acts on when it named requested:
// requested itself when it is not empty, otherwise the composition's one
// account, which is empty in a composition that offers clients accounts.
func (f AccountFeatures) AccountFor(requested domain.AccountID) domain.AccountID {
	if requested != "" {
		return requested
	}
	return f.Account
}

// EveryAccount is the default AccountScope and today's behaviour: clients see
// and choose every account and group, a request acts on exactly the account,
// policy and group it named, lists are not restricted, and clients may change
// the account set. It never refuses a caller.
type EveryAccount struct{}

var _ AccountScope = EveryAccount{}

// Features returns the zero value: no account is fixed, so clients see and
// choose accounts and groups.
func (EveryAccount) Features() AccountFeatures {
	return AccountFeatures{}
}

// Account returns the requested account unchanged, empty included.
func (EveryAccount) Account(
	_ domain.Caller, requested domain.AccountID,
) (domain.AccountID, error) {
	return requested, nil
}

// MissingAccount returns the requested policy unchanged, empty included.
func (EveryAccount) MissingAccount(
	_ domain.Caller, requested domain.MissingAccountPolicy,
) (domain.MissingAccountPolicy, error) {
	return requested, nil
}

// List returns the zero restriction: every list stays unrestricted.
func (EveryAccount) List(
	domain.Caller, domain.AccountID,
) (domain.AccountRestriction, error) {
	return domain.AccountRestriction{}, nil
}

// Visible lets every caller see and act on a resource of any account.
func (EveryAccount) Visible(domain.Caller, domain.AccountID) error { return nil }

// Group accepts every group code.
func (EveryAccount) Group(domain.Caller, string) error { return nil }

// ManageAccounts lets every caller change the account set.
func (EveryAccount) ManageAccounts(domain.Caller) error { return nil }

// AccountFeatures reports the account surfaces the installed AccountScope
// offers clients.
func (s *Service) AccountFeatures() AccountFeatures {
	return s.scope.Features()
}

// listRestriction asks the installed AccountScope for the mandatory account
// restriction of a list the context's caller reads; requested is the exact
// account code the caller named for the list ("" when it named none or
// filtered by a text pattern). A missing caller and a scope refusal are
// returned unchanged.
func (s *Service) listRestriction(
	ctx context.Context, requested domain.AccountID,
) (domain.AccountRestriction, error) {
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return domain.AccountRestriction{}, err
	}
	return s.scope.List(caller, requested)
}

// visibleGroups returns which account groups caller sees, by the account
// scope's list restriction asked with no requested account. The zero
// restriction shows every group and reads nothing. Any other shows a group only
// when the restricted account is one of its members, which one read of that
// account answers: none when it has no group or does not exist. The scope's
// refusal is returned unchanged.
func (s *Service) visibleGroups(
	ctx context.Context, caller domain.Caller,
) (func(group string) bool, error) {
	restriction, err := s.scope.List(caller, "")
	if err != nil {
		return nil, err
	}
	if restriction.Account == "" {
		return func(string) bool { return true }, nil
	}
	page, err := s.node.ListAccountRows(ctx, store.AccountListFilter{
		Restriction: restriction,
	})
	if err != nil {
		return nil, err
	}
	member := ""
	if len(page.Rows) > 0 {
		member = page.Rows[0].Account.GroupCode
	}
	return func(group string) bool { return group != "" && group == member }, nil
}

// restrictedAccount applies a list's account restriction to the exact account
// a list of account-owned rows narrows to through the node ("" for every
// account). The zero restriction keeps account, an omitted account takes the
// restricted one, and any other account leaves no rows to list (ok false).
// Every such row has an account, so IncludeUnowned adds none.
func restrictedAccount(
	account domain.AccountID, restriction domain.AccountRestriction,
) (scoped domain.AccountID, ok bool) {
	switch {
	case restriction.Account == "", account == restriction.Account:
		return account, true
	case account == "":
		return restriction.Account, true
	default:
		return "", false
	}
}
