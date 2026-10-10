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
	"sort"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

// ListAudit returns the most recent count audit rows, newest first, narrowed by
// the account scope's list restriction. The zero restriction reads the node's
// unfiltered listing; any other is read through its filtered one, so count
// bounds the restricted set.
func (s *Service) ListAudit(
	ctx context.Context, count int,
) ([]domain.AuditRow, error) {
	restriction, err := s.listRestriction(ctx, "")
	if err != nil {
		return nil, err
	}
	var rows []domain.AuditRow
	if restriction == (domain.AccountRestriction{}) {
		rows, err = s.node.ListAudit(ctx, count)
	} else {
		rows, err = s.node.ListAuditFiltered(
			ctx, domain.AuditFilter{Restriction: restriction}, count,
		)
	}
	if err != nil {
		return nil, err
	}
	sortAuditNewestFirst(rows)
	return rows, nil
}

// ListAuditFiltered returns audit entries, newest first, narrowed by the
// filter and the account scope's list restriction, which is asked with the
// filter's exact account and replaces any filter.Restriction the caller
// supplies. The restriction and the account, source, and action filters are
// applied in the node's query so count bounds the already-filtered set. A
// zero-value filter matches all rows.
func (s *Service) ListAuditFiltered(
	ctx context.Context, filter domain.AuditFilter, count int,
) ([]domain.AuditRow, error) {
	var err error
	filter.Restriction, err = s.listRestriction(ctx, filter.Account)
	if err != nil {
		return nil, err
	}
	rows, err := s.node.ListAuditFiltered(ctx, filter, count)
	if err != nil {
		return nil, err
	}
	sortAuditNewestFirst(rows)
	return rows, nil
}

// sortAuditNewestFirst orders audit rows newest first by timestamp, breaking
// ties on the opaque external id (descending). The order is fixed here rather
// than left to the node, so it does not depend on the node's own ordering and
// same-instant rows come back in one deterministic order.
func sortAuditNewestFirst(rows []domain.AuditRow) {
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].At.Equal(rows[j].At) {
			return rows[i].At.After(rows[j].At)
		}
		return rows[i].ExternalID.String() > rows[j].ExternalID.String()
	})
}

// ListAuditRows returns audit entries with DB-side filters and counts, narrowed
// by the account scope's list restriction; the account filter is a text
// pattern, so the scope is asked with no requested account. The restriction
// replaces any filter.Restriction the caller supplies.
func (s *Service) ListAuditRows(
	ctx context.Context, filter store.AuditListFilter,
) (store.AuditListPage, error) {
	var err error
	filter.Restriction, err = s.listRestriction(ctx, "")
	if err != nil {
		return store.AuditListPage{}, err
	}
	return s.node.ListAuditRows(ctx, filter)
}
