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

package node

import (
	"context"
	"slices"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

func (r *memoryRealm) AppendAudit(_ context.Context, entry store.AuditEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	row := domain.AuditRow{
		At:           time.Now().UTC(),
		ExternalID:   r.nextExternalID(),
		Actor:        entry.Actor,
		ActorTitle:   entry.ActorTitle,
		Action:       entry.Action,
		Account:      entry.Account,
		AccountTitle: entry.AccountTitle,
		Asset:        entry.Asset,
		Detail:       entry.Detail,
		Source:       entry.Source,
	}
	r.audit = append(r.audit, row)
	return nil
}

func (r *memoryRealm) ListAudit(_ context.Context, n int) ([]domain.AuditRow, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if n <= 0 {
		return []domain.AuditRow{}, nil
	}
	out := make([]domain.AuditRow, 0, min(n, len(r.audit)))
	for i := len(r.audit) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, r.audit[i])
	}
	return out, nil
}

func (r *memoryRealm) ListAuditFiltered(
	_ context.Context, filter domain.AuditFilter, n int,
) ([]domain.AuditRow, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if n <= 0 {
		return []domain.AuditRow{}, nil
	}
	var out []domain.AuditRow
	for i := len(r.audit) - 1; i >= 0 && len(out) < n; i-- {
		row := r.audit[i]
		if filter.Account != "" && row.Account != filter.Account {
			continue
		}
		if filter.Source != "" && row.Source != filter.Source {
			continue
		}
		if len(filter.Actions) > 0 && !slices.Contains(filter.Actions, row.Action) {
			continue
		}
		out = append(out, row)
	}
	return out, nil
}

func (r *memoryRealm) ListAuditRows(
	ctx context.Context, filter store.AuditListFilter,
) (store.AuditListPage, error) {
	rows, err := r.ListAudit(ctx, len(r.audit))
	if err != nil {
		return store.AuditListPage{}, err
	}
	out := make([]domain.AuditRow, 0, len(rows))
	for _, row := range rows {
		if !filter.ExternalID.IsZero() && row.ExternalID != filter.ExternalID {
			continue
		}
		if !textMatches(filter.Account, row.Account.String()) ||
			!textMatches(filter.Asset, row.Asset) ||
			!textMatches(filter.Actor, row.Actor) {
			continue
		}
		if filter.Source != "" && row.Source != filter.Source {
			continue
		}
		if len(filter.Actions) > 0 && !slices.Contains(filter.Actions, row.Action) {
			continue
		}
		out = append(out, row)
	}
	total := len(out)
	if filter.Page.Limit > 0 {
		start := min(max(filter.Page.Offset, 0), len(out))
		end := min(start+filter.Page.Limit, len(out))
		out = out[start:end]
	}
	return store.AuditListPage{Rows: out, Total: total}, nil
}
