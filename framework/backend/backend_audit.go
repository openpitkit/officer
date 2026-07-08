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
	"sort"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

// ListAudit returns the most recent count audit rows aggregated across all
// nodes, newest first. Per-node results are merged, sorted by id desc, then
// bounded to count.
func (s *Service) ListAudit(
	ctx context.Context, count int,
) ([]domain.AuditRow, error) {
	rows := make([]domain.AuditRow, 0)
	for i, n := range s.router.All() {
		part, err := n.ListAudit(ctx, count)
		if err != nil {
			return nil, fmt.Errorf("backend: node %d list audit: %w", i, err)
		}
		rows = append(rows, part...)
	}
	sortAuditNewestFirst(rows)
	if count > 0 && len(rows) > count {
		rows = rows[:count]
	}
	return rows, nil
}

// ListAuditFiltered returns audit entries, newest first, narrowed by the
// filter. The account, source, and action filters are applied in each node's
// query so count bounds the already-filtered set; the per-node results are
// merged, sorted by id desc, then bounded to count. A zero-value filter
// matches all rows.
func (s *Service) ListAuditFiltered(
	ctx context.Context, filter domain.AuditFilter, count int,
) ([]domain.AuditRow, error) {
	rows := make([]domain.AuditRow, 0)
	for i, n := range s.router.All() {
		part, err := n.ListAuditFiltered(ctx, filter, count)
		if err != nil {
			return nil, fmt.Errorf("backend: node %d list audit filtered: %w", i, err)
		}
		rows = append(rows, part...)
	}
	sortAuditNewestFirst(rows)
	if count > 0 && len(rows) > count {
		rows = rows[:count]
	}
	return rows, nil
}

// ListAuditRows returns audit entries with DB-side filters/counts from each
// node.
func (s *Service) ListAuditRows(
	ctx context.Context, filter store.AuditListFilter,
) (store.AuditListPage, error) {
	nodes := s.router.All()
	if len(nodes) == 1 {
		target, ok := nodes[0].(auditRowNode)
		if !ok {
			return store.AuditListPage{}, fmt.Errorf(
				"backend: node list audit rows: %w", domain.ErrNotImplemented,
			)
		}
		return target.ListAuditRows(ctx, filter)
	}
	rows := make([]domain.AuditRow, 0)
	total := 0
	nodeFilter := filter
	nodeFilter.Page = nodePageForMerge(filter.Page)
	for i, n := range nodes {
		target, ok := n.(auditRowNode)
		if !ok {
			return store.AuditListPage{}, fmt.Errorf(
				"backend: node %d list audit rows: %w", i, domain.ErrNotImplemented,
			)
		}
		part, err := target.ListAuditRows(ctx, nodeFilter)
		if err != nil {
			return store.AuditListPage{}, fmt.Errorf(
				"backend: node %d list audit rows: %w", i, err,
			)
		}
		total += part.Total
		rows = append(rows, part.Rows...)
	}
	sortAuditNewestFirst(rows)
	rows = pageAuditRows(rows, filter.Page)
	return store.AuditListPage{Rows: rows, Total: total}, nil
}

// sortAuditNewestFirst orders audit rows newest first by timestamp, breaking
// ties on the opaque external id (descending) for a stable merge across nodes.
// Machine records carry no integer id, so the external id is the tiebreaker.
func sortAuditNewestFirst(rows []domain.AuditRow) {
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].At.Equal(rows[j].At) {
			return rows[i].At.After(rows[j].At)
		}
		return rows[i].ExternalID.String() > rows[j].ExternalID.String()
	})
}
