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
// snapshots, so dictionary deletes do not rewrite history. A nullable account
// identity link supports current-code filtering after account renames. The
// asset code is an immutable snapshot of the asset an action targeted. Listings
// honor the (at DESC, id DESC) index and apply the account, asset, source, and
// action filters in the query so the limit bounds the filtered set.

package store

import (
	"context"
	"database/sql"
	"errors"
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
       au.actor_code, au.actor_title, au.at, au.action, au.source, au.detail
FROM audit au`

// AppendAudit persists a new append-only audit record, assigning the external id
// and timestamp. Account and actor codes are written verbatim; missing
// dictionary rows do not reject the audit write.
func (r *realmStore) AppendAudit(
	ctx context.Context, entry fwstore.AuditEntry,
) error {
	xid, err := newExternalID()
	if err != nil {
		return err
	}
	accountTitle := entry.AccountTitle
	if accountTitle == "" && entry.Account != "" {
		accountTitle = lookupTitle(ctx, r.db(), "account", entry.Account.String())
	}
	actorTitle := entry.ActorTitle
	if actorTitle == "" && entry.Actor != "" {
		actorTitle = lookupTitle(ctx, r.db(), "principal", entry.Actor)
	}
	accountID, err := lookupID(ctx, r.db(), "account", entry.Account.String())
	if err != nil {
		return err
	}
	source := entry.Source
	if source == "" {
		source = domain.SourceSystem
	}
	if _, err := r.db().ExecContext(
		ctx,
		`INSERT INTO audit
		 (external_id, account_id, account_code, account_title, asset_code,
		  actor_code, actor_title, at, action, source, detail)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		xid.Bytes(), accountID, entry.Account.String(), accountTitle, entry.Asset,
		entry.Actor, actorTitle, nowStr(), string(entry.Action), string(source),
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
		q += ` AND (au.account_code = ? OR au.account_id = (
			SELECT id FROM account WHERE code = ?
		))`
		args = append(args, filter.Account.String(), filter.Account.String())
	}
	if filter.Source != "" {
		q += ` AND au.source = ?`
		args = append(args, string(filter.Source))
	}
	if len(filter.Actions) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(filter.Actions)), ",")
		q += fmt.Sprintf(` AND au.action IN (%s)`, placeholders)
		for _, action := range filter.Actions {
			args = append(args, string(action))
		}
	}
	q += ` ORDER BY au.at DESC, au.id DESC LIMIT ?`
	args = append(args, n)

	rows, err := r.db().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list audit: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]domain.AuditRow, 0, n)
	for rows.Next() {
		row, err := scanAuditRow(rows)
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
	clauses, args := auditListClauses(filter)

	countQuery := `SELECT COUNT(*) FROM audit au` + whereFromClauses(clauses)
	var total int
	if err := r.db().QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return fwstore.AuditListPage{}, fmt.Errorf("store: count audit rows: %w", err)
	}

	query, queryArgs := buildAuditListQuery(clauses, args, filter.Page)
	rows, err := r.db().QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return fwstore.AuditListPage{}, fmt.Errorf("store: list audit rows: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]domain.AuditRow, 0)
	for rows.Next() {
		row, err := scanAuditListRow(rows)
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
       au.actor_code, au.actor_title, au.at, au.action, au.source, au.detail
FROM audit au` +
		whereFromClauses(clauses) +
		` ORDER BY au.at DESC, au.id DESC`
	if page.Limit > 0 {
		query += ` LIMIT ? OFFSET ?`
		queryArgs = append(queryArgs, page.Limit, max(page.Offset, 0))
	}
	return query, queryArgs
}

func auditListClauses(filter fwstore.AuditListFilter) ([]string, []any) {
	clauses := make([]string, 0)
	args := make([]any, 0)
	if !filter.ExternalID.IsZero() {
		clauses = append(clauses, "au.external_id = ?")
		args = append(args, filter.ExternalID.Bytes())
	}
	appendAuditAccountMatcher(&clauses, &args, filter.Account)
	appendMatcher(&clauses, &args, "au.asset_code", filter.Asset)
	appendMatcher(&clauses, &args, "au.actor_code", filter.Actor)
	if filter.Source != "" {
		clauses = append(clauses, "au.source = ?")
		args = append(args, string(filter.Source))
	}
	if len(filter.Actions) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(filter.Actions)), ",")
		clauses = append(clauses, fmt.Sprintf("au.action IN (%s)", placeholders))
		for _, action := range filter.Actions {
			args = append(args, string(action))
		}
	} else {
		appendAuditCategoryFilter(&clauses, &args, filter.Category)
	}
	appendTimeRangeFilter(&clauses, &args, "au.at", filter.At)
	return clauses, args
}

func appendAuditCategoryFilter(
	clauses *[]string, args *[]any, category domain.AuditCategory,
) {
	switch category {
	case domain.AuditCategoryControl:
		*clauses = append(*clauses, "au.action NOT IN (?, ?)")
		*args = append(
			*args,
			string(domain.AuditActionSubmitOrder),
			string(domain.AuditActionExecutionReport),
		)
	case domain.AuditCategoryTrading:
		*clauses = append(*clauses, "au.action IN (?, ?)")
		*args = append(
			*args,
			string(domain.AuditActionSubmitOrder),
			string(domain.AuditActionExecutionReport),
		)
	}
}

func appendAuditAccountMatcher(
	clauses *[]string, args *[]any, matcher fwstore.TextMatcher,
) {
	if exact, ok := exactMatcherValue(matcher); ok {
		*clauses = append(*clauses, `(au.account_code = ? OR au.account_id = (
			SELECT id FROM account WHERE code = ?
		))`)
		*args = append(*args, exact, exact)
		return
	}
	appendMatcher(clauses, args, "au.account_code", matcher)
}

func exactMatcherValue(matcher fwstore.TextMatcher) (string, bool) {
	if !matcher.AnchorStart || !matcher.AnchorEnd || len(matcher.Fragments) != 1 {
		return "", false
	}
	value := matcher.Fragments[0]
	return value, value != ""
}

func scanAuditListRow(rows *sql.Rows) (domain.AuditRow, error) {
	var (
		extID                                           []byte
		account, accountTitle, asset, actor, actorTitle string
		at, action, source                              string
		detail                                          string
	)
	if err := rows.Scan(
		&extID, &account, &accountTitle, &asset, &actor,
		&actorTitle, &at, &action, &source, &detail,
	); err != nil {
		return domain.AuditRow{}, fmt.Errorf("store: scan audit row: %w", err)
	}
	row, err := auditRowFromScanned(extID, account, accountTitle, asset, actor, actorTitle, at, action, source, detail)
	if err != nil {
		return domain.AuditRow{}, err
	}
	return row, nil
}

// scanAuditRow scans one audit projection, decoding the external-id BLOB and the
// at timestamp.
func scanAuditRow(rows *sql.Rows) (domain.AuditRow, error) {
	var (
		extID                                           []byte
		account, accountTitle, asset, actor, actorTitle string
		at, action, source                              string
		detail                                          string
	)
	if err := rows.Scan(
		&extID, &account, &accountTitle, &asset, &actor, &actorTitle,
		&at, &action, &source, &detail,
	); err != nil {
		return domain.AuditRow{}, fmt.Errorf("store: scan audit: %w", err)
	}
	return auditRowFromScanned(extID, account, accountTitle, asset, actor, actorTitle, at, action, source, detail)
}

func auditRowFromScanned(
	extID []byte,
	account string,
	accountTitle string,
	asset string,
	actor string,
	actorTitle string,
	at string,
	action string,
	source string,
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
	return domain.AuditRow{
		ExternalID:   xid,
		Account:      domain.AccountID(account),
		AccountTitle: accountTitle,
		Asset:        asset,
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

func lookupID(
	ctx context.Context, q sqlQueryer, table, code string,
) (any, error) {
	if code == "" {
		return nil, nil
	}
	var id int64
	query := fmt.Sprintf(`SELECT id FROM %s WHERE code = ?`, table)
	if err := q.QueryRowContext(ctx, query, code).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("store: lookup %s id for %q: %w", table, code, err)
	}
	return id, nil
}
