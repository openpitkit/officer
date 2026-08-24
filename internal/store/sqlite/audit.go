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

// Audit group of the SQLite store: the append-only compliance trail. An audit
// row is a machine record addressed by its opaque external id; the surrogate id
// never crosses the interface boundary. Account and actor strings are immutable
// snapshots, so dictionary deletes do not rewrite history. Account, asset, and
// group filters use those captured strings rather than following a mutable
// identity link. Listings honor the (at DESC, id DESC) index and apply the
// account, asset, source, and action filters in the query so the limit bounds
// the filtered set.

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"go.openpit.dev/officer/framework/domain"
	fwstore "go.openpit.dev/officer/framework/store"
)

// auditSelect is the shared projection for audit reads. The surrogate id is
// never selected.
const auditSelect = `
SELECT au.external_id, au.account_code, au.account_title, au.asset_code,
       au.group_code, au.actor_code, au.actor_title, au.at, au.action_id,
       au.source_id, au.detail
FROM audit au`

// AppendAudit persists a new append-only audit record, assigning the external id
// and timestamp. Account and actor codes are written verbatim; missing
// dictionary rows do not reject the audit write.
func (r *realmStore) AppendAudit(
	ctx context.Context, entry fwstore.AuditEntry,
) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	dictionaries, err := r.dictionaries()
	if err != nil {
		return err
	}
	return appendAudit(ctx, db, dictionaries, entry)
}

// AppendAuditBatch persists all entries in one transaction, so a multi-row
// compliance event is either wholly visible or wholly absent.
func (r *realmStore) AppendAuditBatch(
	ctx context.Context, entries []fwstore.AuditEntry,
) (err error) {
	if len(entries) == 0 {
		return nil
	}
	db, err := r.db()
	if err != nil {
		return err
	}
	dictionaries, err := r.dictionaries()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin audit batch: %w", err)
	}
	defer rollbackTransaction(&err, tx)
	for _, entry := range entries {
		if err := appendAudit(ctx, tx, dictionaries, entry); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit audit batch: %w", err)
	}
	return nil
}

func appendAudit(
	ctx context.Context,
	q sqlReadWriter,
	dictionaries *enumDictionaries,
	entry fwstore.AuditEntry,
) error {
	xid, err := newExternalID()
	if err != nil {
		return err
	}
	accountTitle := entry.AccountTitle
	if accountTitle == "" && entry.Account != "" {
		accountTitle = lookupTitle(ctx, q, "account", entry.Account.String())
	}
	actorTitle := entry.ActorTitle
	if actorTitle == "" && entry.Actor != "" {
		actorTitle = lookupTitle(ctx, q, "principal", entry.Actor)
	}
	source := entry.Source
	if source == "" {
		source = domain.SourceSystem
	}
	actionID, err := dictionaries.id(auditActionTable, "audit action", string(entry.Action))
	if err != nil {
		return err
	}
	sourceID, err := dictionaries.id(sourceKindTable, "source", string(source))
	if err != nil {
		return err
	}
	if _, err := q.ExecContext(
		ctx,
		`INSERT INTO audit
		 (external_id, account_code, account_title, asset_code,
		  group_code, actor_code, actor_title, at, action_id, source_id, detail)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		xid.Bytes(), entry.Account.String(), accountTitle, entry.Asset,
		entry.Group, entry.Actor, actorTitle, nowStr(), actionID, sourceID,
		entry.Detail,
	); err != nil {
		return fmt.Errorf("store: append audit: %w", err)
	}
	return nil
}

// ListAudit returns the most recent n audit rows, newest first by (at, id) per
// the §9 index. A non-positive n returns an empty slice.
func (r *realmStore) ListAudit(ctx context.Context, n int) ([]domain.AuditRow, error) {
	return r.ListAuditFiltered(ctx, domain.AuditFilter{}, n)
}

// ListAuditFiltered returns the most recent n audit rows matching the filter,
// newest first by (at, id). The account, source, and action filters are applied
// in the query so the limit bounds the filtered set, not a pre-filter window. A
// zero-value filter matches all rows; a non-positive n returns an empty slice.
func (r *realmStore) ListAuditFiltered(
	ctx context.Context, filter domain.AuditFilter, n int,
) ([]domain.AuditRow, error) {
	if n <= 0 {
		return make([]domain.AuditRow, 0), nil
	}

	q := auditSelect + ` WHERE 1 = 1`
	args := make([]any, 0, 4)
	if filter.Account != "" {
		q += ` AND au.account_code = ?`
		args = append(args, filter.Account.String())
	}
	if filter.Source != "" {
		dictionaries, err := r.dictionaries()
		if err != nil {
			return nil, err
		}
		sourceID, err := dictionaries.id(sourceKindTable, "source", string(filter.Source))
		if err != nil {
			return nil, err
		}
		q += ` AND au.source_id = ?`
		args = append(args, sourceID)
	}
	if len(filter.Actions) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(filter.Actions)), ",")
		q += fmt.Sprintf(` AND au.action_id IN (%s)`, placeholders)
		dictionaries, err := r.dictionaries()
		if err != nil {
			return nil, err
		}
		for _, action := range filter.Actions {
			id, err := dictionaries.id(auditActionTable, "audit action", string(action))
			if err != nil {
				return nil, err
			}
			args = append(args, id)
		}
	}
	q += ` ORDER BY au.at DESC, au.id DESC LIMIT ?`
	args = append(args, n)

	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list audit: %w", err)
	}
	defer func() { _ = rows.Close() }()
	dictionaries, err := r.dictionaries()
	if err != nil {
		return nil, err
	}

	result := make([]domain.AuditRow, 0, n)
	for rows.Next() {
		row, err := scanAuditRow(dictionaries, rows)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate audit: %w", err)
	}
	return result, nil
}

// ListAuditRows returns audit rows matching filter, with total count before
// paging.
func (r *realmStore) ListAuditRows(
	ctx context.Context, filter fwstore.AuditListFilter,
) (fwstore.AuditListPage, error) {
	dictionaries, err := r.dictionaries()
	if err != nil {
		return fwstore.AuditListPage{}, err
	}
	clauses, args, err := auditListClauses(dictionaries, filter)
	if err != nil {
		return fwstore.AuditListPage{}, err
	}

	db, err := r.db()
	if err != nil {
		return fwstore.AuditListPage{}, err
	}
	countQuery := `SELECT COUNT(*) FROM audit au` + whereFromClauses(clauses)
	var total int
	if err := db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return fwstore.AuditListPage{}, fmt.Errorf("store: count audit rows: %w", err)
	}

	query, queryArgs := buildAuditListQuery(clauses, args, filter.Page)
	rows, err := db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return fwstore.AuditListPage{}, fmt.Errorf("store: list audit rows: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]domain.AuditRow, 0)
	for rows.Next() {
		row, err := scanAuditListRow(dictionaries, rows)
		if err != nil {
			return fwstore.AuditListPage{}, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return fwstore.AuditListPage{}, fmt.Errorf("store: iterate audit rows: %w", err)
	}
	return fwstore.AuditListPage{
		Rows:  result,
		Total: total,
	}, nil
}

// buildAuditListQuery assembles the audit list query and its bound args from
// the pre-built filter clauses. Extracted so tests can EXPLAIN the exact query
// the list path runs.
func buildAuditListQuery(
	clauses []string,
	args []any,
	page fwstore.PageSpec,
) (string, []any) {
	queryArgs := append([]any{}, args...)
	query := `
SELECT au.external_id, au.account_code, au.account_title, au.asset_code,
       au.group_code, au.actor_code, au.actor_title, au.at, au.action_id,
       au.source_id, au.detail
FROM audit au` +
		whereFromClauses(clauses) +
		` ORDER BY au.at DESC, au.id DESC`
	if page.Limit > 0 {
		query += ` LIMIT ? OFFSET ?`
		queryArgs = append(queryArgs, page.Limit, max(page.Offset, 0))
	}
	return query, queryArgs
}

func auditListClauses(
	dictionaries *enumDictionaries,
	filter fwstore.AuditListFilter,
) ([]string, []any, error) {
	clauses := make([]string, 0)
	args := make([]any, 0)
	if !filter.ExternalID.IsZero() {
		clauses = append(clauses, "au.external_id = ?")
		args = append(args, filter.ExternalID.Bytes())
	}
	appendMatcher(&clauses, &args, "au.account_code", filter.Account)
	appendMatcher(&clauses, &args, "au.asset_code", filter.Asset)
	appendMatcher(&clauses, &args, "au.group_code", filter.Group)
	appendMatcher(&clauses, &args, "au.actor_code", filter.Actor)
	if filter.Source != "" {
		id, err := dictionaries.id(sourceKindTable, "source", string(filter.Source))
		if err != nil {
			return nil, nil, err
		}
		clauses = append(clauses, "au.source_id = ?")
		args = append(args, id)
	}
	if len(filter.Actions) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(filter.Actions)), ",")
		clauses = append(clauses, fmt.Sprintf("au.action_id IN (%s)", placeholders))
		for _, action := range filter.Actions {
			id, err := dictionaries.id(auditActionTable, "audit action", string(action))
			if err != nil {
				return nil, nil, err
			}
			args = append(args, id)
		}
	} else {
		if err := appendAuditCategoryFilter(dictionaries, &clauses, &args, filter.Category); err != nil {
			return nil, nil, err
		}
	}
	appendTimeRangeFilter(&clauses, &args, "au.at", filter.At)
	return clauses, args, nil
}

func appendAuditCategoryFilter(
	dictionaries *enumDictionaries,
	clauses *[]string,
	args *[]any,
	category domain.AuditCategory,
) error {
	submitID, err := dictionaries.id(
		auditActionTable, "audit action", string(domain.AuditActionSubmitOrder),
	)
	if err != nil {
		return err
	}
	executionID, err := dictionaries.id(
		auditActionTable, "audit action", string(domain.AuditActionExecutionReport),
	)
	if err != nil {
		return err
	}
	switch category {
	case domain.AuditCategoryControl:
		*clauses = append(*clauses, "au.action_id NOT IN (?, ?)")
		*args = append(*args, submitID, executionID)
	case domain.AuditCategoryTrading:
		*clauses = append(*clauses, "au.action_id IN (?, ?)")
		*args = append(*args, submitID, executionID)
	}
	return nil
}

func scanAuditListRow(
	dictionaries *enumDictionaries,
	rows *sql.Rows,
) (domain.AuditRow, error) {
	var (
		extID                                                  []byte
		account, accountTitle, asset, group, actor, actorTitle string
		at                                                     string
		actionID, sourceID                                     int64
		detail                                                 string
	)
	if err := rows.Scan(
		&extID, &account, &accountTitle, &asset, &group, &actor,
		&actorTitle, &at, &actionID, &sourceID, &detail,
	); err != nil {
		return domain.AuditRow{}, fmt.Errorf("store: scan audit row: %w", err)
	}
	row, err := auditRowFromScanned(
		dictionaries, extID, account, accountTitle, asset, group, actor, actorTitle,
		at, actionID, sourceID, detail,
	)
	if err != nil {
		return domain.AuditRow{}, err
	}
	return row, nil
}

// scanAuditRow scans one audit projection, decoding the external-id BLOB and the
// at timestamp.
func scanAuditRow(dictionaries *enumDictionaries, rows *sql.Rows) (domain.AuditRow, error) {
	var (
		extID                                                  []byte
		account, accountTitle, asset, group, actor, actorTitle string
		at                                                     string
		actionID, sourceID                                     int64
		detail                                                 string
	)
	if err := rows.Scan(
		&extID, &account, &accountTitle, &asset, &group, &actor, &actorTitle,
		&at, &actionID, &sourceID, &detail,
	); err != nil {
		return domain.AuditRow{}, fmt.Errorf("store: scan audit: %w", err)
	}
	return auditRowFromScanned(
		dictionaries, extID, account, accountTitle, asset, group, actor, actorTitle,
		at, actionID, sourceID, detail,
	)
}

func auditRowFromScanned(
	dictionaries *enumDictionaries,
	extID []byte,
	account string,
	accountTitle string,
	asset string,
	group string,
	actor string,
	actorTitle string,
	at string,
	actionID int64,
	sourceID int64,
	detail string,
) (domain.AuditRow, error) {
	xid, err := domain.ExternalIDFromBytes(extID)
	if err != nil {
		return domain.AuditRow{}, fmt.Errorf("store: decode audit external id: %w", err)
	}
	parsedAt, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return domain.AuditRow{}, fmt.Errorf("store: parse audit at %q: %w", at, err)
	}
	action, err := dictionaries.code(auditActionTable, "audit action", actionID)
	if err != nil {
		return domain.AuditRow{}, err
	}
	source, err := dictionaries.code(sourceKindTable, "source", sourceID)
	if err != nil {
		return domain.AuditRow{}, err
	}
	return domain.AuditRow{
		ExternalID:   xid,
		Account:      domain.AccountID(account),
		AccountTitle: accountTitle,
		Asset:        asset,
		Group:        group,
		Actor:        actor,
		ActorTitle:   actorTitle,
		At:           parsedAt,
		Action:       domain.AuditAction(action),
		Source:       domain.Source(source),
		Detail:       detail,
	}, nil
}

func lookupTitle(ctx context.Context, q sqlQueryer, table, code string) string {
	var title string
	query := fmt.Sprintf(`SELECT title FROM %s WHERE code = ?`, table)
	if err := q.QueryRowContext(ctx, query, code).Scan(&title); err != nil {
		return ""
	}
	return title
}
