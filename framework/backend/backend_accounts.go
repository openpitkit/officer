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
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
)

// ListAccounts returns every account.
func (s *Service) ListAccounts(ctx context.Context) ([]domain.Account, error) {
	page, err := s.ListAccountRows(ctx, store.AccountListFilter{})
	if err != nil {
		return nil, err
	}
	accounts := make([]domain.Account, 0, len(page.Rows))
	for _, row := range page.Rows {
		accounts = append(accounts, row.Account)
	}
	return accounts, nil
}

// ListAccountRows returns accounts with list-only counts.
func (s *Service) ListAccountRows(
	ctx context.Context, filter store.AccountListFilter,
) (store.AccountListPage, error) {
	return s.node.ListAccountRows(ctx, filter)
}

// CreateAccount validates the account metadata and creates the account.
func (s *Service) CreateAccount(
	ctx context.Context, account domain.Account,
) (domain.Account, error) {
	id := account.Code
	if err := domain.WithValidationPointer(
		"/code", domain.ValidateAccountID(id),
	); err != nil {
		return domain.Account{}, err
	}
	if err := domain.ValidateTitle(account.Title); err != nil {
		return domain.Account{}, err
	}
	if err := domain.ValidateBlockReason(account.BlockReason); err != nil {
		return domain.Account{}, err
	}
	if err := validateOptionalCurrency(account.Currency); err != nil {
		return domain.Account{}, err
	}
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return domain.Account{}, err
	}
	return s.node.CreateAccount(ctx, account, caller)
}

// UpdateAccount validates the old and new account metadata and updates the
// account dictionary row.
func (s *Service) UpdateAccount(
	ctx context.Context,
	oldID domain.AccountID,
	account domain.Account,
) (domain.Account, error) {
	if err := domain.ValidateAccountID(oldID); err != nil {
		return domain.Account{}, err
	}
	if err := domain.WithValidationPointer(
		"/code", domain.ValidateAccountID(account.Code),
	); err != nil {
		return domain.Account{}, err
	}
	if err := domain.ValidateTitle(account.Title); err != nil {
		return domain.Account{}, err
	}
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return domain.Account{}, err
	}
	return s.node.UpdateAccount(
		ctx,
		oldID,
		account,
		caller,
	)
}

// BlockAccount validates the id and blocks the account with reason. missing is
// the caller's required choice for an account that does not exist yet.
func (s *Service) BlockAccount(
	ctx context.Context,
	id domain.AccountID,
	reason string,
	missing domain.MissingAccountPolicy,
) error {
	return s.setAccountBlocked(ctx, id, true, reason, missing)
}

// UnblockAccount validates the id and unblocks the account. missing follows
// BlockAccount.
func (s *Service) UnblockAccount(
	ctx context.Context, id domain.AccountID, missing domain.MissingAccountPolicy,
) error {
	return s.setAccountBlocked(ctx, id, false, "", missing)
}

// DeleteAccount validates the id and removes the account. Destructive cascades
// require force.
func (s *Service) DeleteAccount(
	ctx context.Context, id domain.AccountID, force bool,
) error {
	if err := domain.ValidateAccountID(id); err != nil {
		return err
	}
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return err
	}
	return s.node.DeleteAccount(ctx, id, force, caller)
}

func (s *Service) setAccountBlocked(
	ctx context.Context, id domain.AccountID, blocked bool, reason string,
	missing domain.MissingAccountPolicy,
) error {
	if err := domain.ValidateAccountID(id); err != nil {
		return err
	}
	if err := domain.ValidateBlockReason(reason); err != nil {
		return err
	}
	if err := validateMissingAccountPolicy(id, missing); err != nil {
		return err
	}
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return err
	}
	return s.node.SetAccountBlocked(
		ctx, id, blocked, reason, missing, caller,
	)
}

// GetAccountState validates the id and returns the account row and its
// account-scoped typed barriers.
func (s *Service) GetAccountState(
	ctx context.Context, id domain.AccountID,
) (domain.Account, node.AccountLimits, error) {
	if err := domain.ValidateAccountID(id); err != nil {
		return domain.Account{}, node.AccountLimits{}, err
	}
	return s.node.GetAccountState(ctx, id)
}

// SetAccountGroup validates the account id and sets or clears (empty
// groupCode) the account's group membership by the group's code.
// missing is the caller's required choice for an account that does not exist yet.
func (s *Service) SetAccountGroup(
	ctx context.Context,
	id domain.AccountID,
	groupCode string,
	missing domain.MissingAccountPolicy,
) error {
	if err := domain.ValidateAccountID(id); err != nil {
		return err
	}
	if err := validateMissingAccountPolicy(id, missing); err != nil {
		return err
	}
	if groupCode != "" {
		if err := domain.WithValidationPointer(
			"/group", domain.ValidateGroupID(groupCode),
		); err != nil {
			return err
		}
	}
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return err
	}
	return s.node.SetAccountGroup(
		ctx, id, groupCode, missing, caller,
	)
}

// SetAccountCurrency validates and sets or clears the account-level currency.
func (s *Service) SetAccountCurrency(
	ctx context.Context, id domain.AccountID, currency string,
) error {
	if err := domain.ValidateAccountID(id); err != nil {
		return err
	}
	if err := validateOptionalCurrency(currency); err != nil {
		return err
	}
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return err
	}
	return s.node.SetAccountCurrency(ctx, id, currency, caller)
}

// SetAccountNotes validates the account id and notes and replaces the
// account's free-form notes.
func (s *Service) SetAccountNotes(
	ctx context.Context, id domain.AccountID, notes string,
) error {
	if err := domain.ValidateAccountID(id); err != nil {
		return err
	}
	if err := domain.ValidateNotes(notes); err != nil {
		return err
	}
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return err
	}
	return s.node.SetAccountNotes(ctx, id, notes, caller)
}
