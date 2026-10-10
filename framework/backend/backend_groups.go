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
	"fmt"
	"slices"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

// CreateGroup validates the group metadata and creates the group, returning the
// stored group with its engine group id populated.
func (s *Service) CreateGroup(
	ctx context.Context, group domain.AccountGroup,
) (domain.AccountGroup, error) {
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return domain.AccountGroup{}, err
	}
	if err := s.scope.ManageAccounts(caller); err != nil {
		return domain.AccountGroup{}, err
	}
	if err := domain.ValidateGroupID(group.Code); err != nil {
		return domain.AccountGroup{}, err
	}
	if err := domain.ValidateTitle(group.Title); err != nil {
		return domain.AccountGroup{}, err
	}
	if err := domain.ValidateNotes(group.Notes); err != nil {
		return domain.AccountGroup{}, err
	}
	if err := domain.ValidateBlockReason(group.BlockReason); err != nil {
		return domain.AccountGroup{}, err
	}
	if err := validateOptionalCurrency(group.Currency); err != nil {
		return domain.AccountGroup{}, err
	}
	return s.node.CreateGroup(ctx, group, caller)
}

// UpdateGroup validates the old and new group metadata and updates the group.
func (s *Service) UpdateGroup(
	ctx context.Context,
	oldCode string,
	group domain.AccountGroup,
) (domain.AccountGroup, error) {
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return domain.AccountGroup{}, err
	}
	if err := s.scope.ManageAccounts(caller); err != nil {
		return domain.AccountGroup{}, err
	}
	if err := domain.ValidateGroupID(oldCode); err != nil {
		return domain.AccountGroup{}, err
	}
	if err := domain.ValidateGroupID(group.Code); err != nil {
		return domain.AccountGroup{}, err
	}
	if err := domain.ValidateTitle(group.Title); err != nil {
		return domain.AccountGroup{}, err
	}
	return s.node.UpdateGroup(ctx, oldCode, group, caller)
}

// ListGroups returns the groups in the realm within the account scope's list
// restriction, read through ListGroupRows.
func (s *Service) ListGroups(ctx context.Context) ([]domain.AccountGroup, error) {
	page, err := s.ListGroupRows(ctx, store.GroupListFilter{})
	if err != nil {
		return nil, err
	}
	groups := make([]domain.AccountGroup, 0, len(page.Rows))
	for _, row := range page.Rows {
		groups = append(groups, row.Group)
	}
	return groups, nil
}

// ListGroupRows returns groups in the realm with list-only counts, narrowed by
// the account scope's list restriction: a restricted list keeps only the
// groups the restricted account is a member of and counts that member alone.
// The code filter is a text pattern, so the scope is asked with no requested
// account, and the restriction replaces any filter.Restriction the caller
// supplies. The page passes through from the node unchanged; sort and paging
// are applied by the connector.
func (s *Service) ListGroupRows(
	ctx context.Context, filter store.GroupListFilter,
) (store.GroupListPage, error) {
	var err error
	filter.Restriction, err = s.listRestriction(ctx, "")
	if err != nil {
		return store.GroupListPage{}, err
	}
	n := s.node
	page, err := n.ListGroupRows(ctx, filter)
	if err != nil {
		return store.GroupListPage{}, fmt.Errorf("backend: list group rows: %w", err)
	}
	return page, nil
}

// GetGroup validates the code and returns the group and its member accounts,
// narrowed by the account scope's list restriction: a restricted caller sees a
// group only when the restricted account is one of its members, and then sees
// that member alone. It maps a missing group, and a group the restriction
// hides, onto the same domain.ErrNotFound, which names no account.
func (s *Service) GetGroup(
	ctx context.Context, code string,
) (domain.AccountGroup, []domain.Account, error) {
	if err := domain.ValidateGroupID(code); err != nil {
		return domain.AccountGroup{}, nil, err
	}
	restriction, err := s.listRestriction(ctx, "")
	if err != nil {
		return domain.AccountGroup{}, nil, err
	}
	n := s.node
	group, accounts, ok, err := n.GetGroup(ctx, code)
	if err != nil {
		return domain.AccountGroup{}, nil, fmt.Errorf("backend: get group: %w", err)
	}
	if ok && restriction.Account != "" {
		accounts = slices.DeleteFunc(slices.Clone(accounts), func(member domain.Account) bool {
			return member.Code != restriction.Account
		})
		ok = len(accounts) > 0
	}
	if !ok {
		return domain.AccountGroup{}, nil, fmt.Errorf("group %q: %w", code, domain.ErrNotFound)
	}
	return group, accounts, nil
}

// SetGroupNotes validates the code and notes and replaces the group's notes.
func (s *Service) SetGroupNotes(ctx context.Context, code, notes string) error {
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return err
	}
	if err := s.scope.ManageAccounts(caller); err != nil {
		return err
	}
	if err := domain.ValidateGroupID(code); err != nil {
		return err
	}
	if err := domain.ValidateNotes(notes); err != nil {
		return err
	}
	return s.node.SetGroupNotes(ctx, code, notes, caller)
}

// SetGroupCurrency validates and sets or clears a group-level currency.
func (s *Service) SetGroupCurrency(ctx context.Context, code, currency string) error {
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return err
	}
	if err := s.scope.ManageAccounts(caller); err != nil {
		return err
	}
	if err := domain.ValidateGroupID(code); err != nil {
		return err
	}
	if err := validateOptionalCurrency(currency); err != nil {
		return err
	}
	return s.node.SetGroupCurrency(ctx, code, currency, caller)
}

// SetDefaultGroupCurrency sets or clears the reserved default group currency.
func (s *Service) SetDefaultGroupCurrency(ctx context.Context, currency string) error {
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return err
	}
	if err := s.scope.ManageAccounts(caller); err != nil {
		return err
	}
	if err := validateOptionalCurrency(currency); err != nil {
		return err
	}
	return s.node.SetDefaultGroupCurrency(ctx, currency, caller)
}

// SetGroupBlocked validates the code and blocks or unblocks the group with
// reason.
func (s *Service) SetGroupBlocked(
	ctx context.Context, code string, blocked bool, reason string,
) error {
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return err
	}
	if err := s.scope.ManageAccounts(caller); err != nil {
		return err
	}
	if err := domain.ValidateGroupID(code); err != nil {
		return err
	}
	if err := domain.ValidateBlockReason(reason); err != nil {
		return err
	}
	return s.node.SetGroupBlocked(ctx, code, blocked, reason, caller)
}

// DeleteGroup validates the code and removes the group. Destructive
// group-owned cascades require force.
func (s *Service) DeleteGroup(ctx context.Context, code string, force bool) error {
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return err
	}
	if err := s.scope.ManageAccounts(caller); err != nil {
		return err
	}
	if err := domain.ValidateGroupID(code); err != nil {
		return err
	}
	return s.node.DeleteGroup(ctx, code, force, caller)
}

func validateOptionalCurrency(currency string) error {
	if currency == "" {
		return nil
	}
	return domain.ValidateAsset(currency)
}
