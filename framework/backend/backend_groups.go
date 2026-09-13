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

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

// CreateGroup validates the group metadata and creates the group, returning the
// stored group with its engine group id populated.
func (s *Service) CreateGroup(
	ctx context.Context, group domain.AccountGroup,
) (domain.AccountGroup, error) {
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
	return s.node.CreateGroup(ctx, group, auth.CallerFromContext(ctx))
}

// UpdateGroup validates the old and new group metadata and updates the group.
func (s *Service) UpdateGroup(
	ctx context.Context,
	oldCode string,
	group domain.AccountGroup,
) (domain.AccountGroup, error) {
	if err := domain.ValidateGroupID(oldCode); err != nil {
		return domain.AccountGroup{}, err
	}
	if err := domain.ValidateGroupID(group.Code); err != nil {
		return domain.AccountGroup{}, err
	}
	if err := domain.ValidateTitle(group.Title); err != nil {
		return domain.AccountGroup{}, err
	}
	return s.node.UpdateGroup(ctx, oldCode, group, auth.CallerFromContext(ctx))
}

// ListGroups returns every group in the realm.
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

// ListGroupRows returns groups in the realm with list-only counts. The page
// passes through from the node unchanged; sort and paging are applied by the
// connector.
func (s *Service) ListGroupRows(
	ctx context.Context, filter store.GroupListFilter,
) (store.GroupListPage, error) {
	n := s.node
	page, err := n.ListGroupRows(ctx, filter)
	if err != nil {
		return store.GroupListPage{}, fmt.Errorf("backend: list group rows: %w", err)
	}
	return page, nil
}

// GetGroup validates the code and returns the group and its member accounts. It
// maps a missing group onto domain.ErrNotFound.
func (s *Service) GetGroup(
	ctx context.Context, code string,
) (domain.AccountGroup, []domain.Account, error) {
	if err := domain.ValidateGroupID(code); err != nil {
		return domain.AccountGroup{}, nil, err
	}
	n := s.node
	group, accounts, ok, err := n.GetGroup(ctx, code)
	if err != nil {
		return domain.AccountGroup{}, nil, fmt.Errorf("backend: get group: %w", err)
	}
	if !ok {
		return domain.AccountGroup{}, nil, fmt.Errorf("group %q: %w", code, domain.ErrNotFound)
	}
	return group, accounts, nil
}

// SetGroupNotes validates the code and notes and replaces the group's notes.
func (s *Service) SetGroupNotes(ctx context.Context, code, notes string) error {
	if err := domain.ValidateGroupID(code); err != nil {
		return err
	}
	if err := domain.ValidateNotes(notes); err != nil {
		return err
	}
	return s.node.SetGroupNotes(ctx, code, notes, auth.CallerFromContext(ctx))
}

// SetGroupCurrency validates and sets or clears a group-level currency.
func (s *Service) SetGroupCurrency(ctx context.Context, code, currency string) error {
	if err := domain.ValidateGroupID(code); err != nil {
		return err
	}
	if err := validateOptionalCurrency(currency); err != nil {
		return err
	}
	return s.node.SetGroupCurrency(ctx, code, currency, auth.CallerFromContext(ctx))
}

// SetDefaultGroupCurrency sets or clears the reserved default group currency.
func (s *Service) SetDefaultGroupCurrency(ctx context.Context, currency string) error {
	if err := validateOptionalCurrency(currency); err != nil {
		return err
	}
	return s.node.SetDefaultGroupCurrency(ctx, currency, auth.CallerFromContext(ctx))
}

// SetGroupBlocked validates the code and blocks or unblocks the group with
// reason.
func (s *Service) SetGroupBlocked(
	ctx context.Context, code string, blocked bool, reason string,
) error {
	if err := domain.ValidateGroupID(code); err != nil {
		return err
	}
	if err := domain.ValidateBlockReason(reason); err != nil {
		return err
	}
	return s.node.SetGroupBlocked(ctx, code, blocked, reason, auth.CallerFromContext(ctx))
}

// DeleteGroup validates the code and removes the group. Destructive
// group-owned cascades require force.
func (s *Service) DeleteGroup(ctx context.Context, code string, force bool) error {
	if err := domain.ValidateGroupID(code); err != nil {
		return err
	}
	return s.node.DeleteGroup(ctx, code, force, auth.CallerFromContext(ctx))
}

func validateOptionalCurrency(currency string) error {
	if currency == "" {
		return nil
	}
	return domain.ValidateAsset(currency)
}
