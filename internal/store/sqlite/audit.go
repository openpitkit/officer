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

// Audit group of the SQLite store: the append-only compliance trail. An audit
// row is a machine record addressed by its opaque external id; the surrogate id
// never crosses the interface boundary. Account and actor strings are immutable
// snapshots, so dictionary deletes do not rewrite history. Account, asset, and
// group filters use those captured strings rather than following a mutable
// identity link. Listings honor the (at DESC, id DESC) index and apply the
// account restriction and the account, asset, source, and action filters in
// the query so the limit bounds the filtered set.

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
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
       au.source_id, au.detail, au.detail_code, au.detail_params,
       au.detail_fields, au.order_id, au.verdict, au.reject_code
FROM audit au`

// auditRestrictionColumn and auditUnownedPredicate address the account
// snapshot in the account restriction. The unary plus keeps SQLite from
// choosing idx_audit_account_code for the restriction's IN-like OR, which
// returns rows out of (at, id) order and adds a temp B-tree sort; the
// restriction on its own keeps the ordered idx_audit_at scan.
const (
	auditRestrictionColumn = "+au.account_code"
	auditUnownedPredicate  = "+au.account_code = ''"
)

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

func validateAuditDecision(
	action domain.AuditAction,
	orderID, verdict, rejectCode string,
) error {
	return domain.ValidateAuditDecision(action, orderID, verdict, rejectCode)
}

func appendAudit(
	ctx context.Context,
	q sqlReadWriter,
	dictionaries *enumDictionaries,
	entry fwstore.AuditEntry,
) error {
	if err := requireSource("audit", entry.Source); err != nil {
		return err
	}
	if err := validateAuditDecision(
		entry.Action, entry.OrderID, entry.Verdict, entry.RejectCode,
	); err != nil {
		return err
	}
	if err := entry.Detail.Validate(); err != nil {
		return fmt.Errorf("store: append audit: %w", err)
	}
	detailParams, detailFields, err := encodeAuditDetail(entry.Detail)
	if err != nil {
		return err
	}
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
	actionID, err := dictionaries.id(auditActionTable, "audit action", string(entry.Action))
	if err != nil {
		return err
	}
	sourceID, err := dictionaries.id(sourceKindTable, "source", string(entry.Source))
	if err != nil {
		return err
	}
	if _, err := q.ExecContext(
		ctx,
		`INSERT INTO audit
		 (external_id, account_code, account_title, asset_code,
		  group_code, actor_code, actor_title, at, action_id, source_id, detail,
		  detail_code, detail_params, detail_fields, order_id, verdict, reject_code)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		xid.Bytes(), entry.Account.String(), accountTitle, entry.Asset,
		entry.Group, entry.Actor, actorTitle, nowStr(), actionID, sourceID,
		entry.Detail.Text, string(entry.Detail.Code), detailParams, detailFields,
		entry.OrderID, entry.Verdict, entry.RejectCode,
	); err != nil {
		return fmt.Errorf("store: append audit: %w", err)
	}
	return nil
}

// ListAudit returns the most recent n audit rows, newest first by the (at, id)
// index. A non-positive n returns an empty slice.
func (r *realmStore) ListAudit(ctx context.Context, n int) ([]domain.AuditRow, error) {
	return r.ListAuditFiltered(ctx, domain.AuditFilter{}, n)
}

// ListAuditFiltered returns the most recent n audit rows matching the filter,
// newest first by (at, id). The account restriction and the account, source,
// and action filters are applied in the query so the limit bounds the filtered
// set, not a pre-filter window. A zero-value filter matches all rows; a
// non-positive n returns an empty slice.
func (r *realmStore) ListAuditFiltered(
	ctx context.Context, filter domain.AuditFilter, n int,
) ([]domain.AuditRow, error) {
	if n <= 0 {
		return make([]domain.AuditRow, 0), nil
	}

	dictionaries, err := r.dictionaries()
	if err != nil {
		return nil, err
	}
	q, args, err := buildAuditFilteredQuery(dictionaries, filter, n)
	if err != nil {
		return nil, err
	}

	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list audit: %w", err)
	}
	defer func() { _ = rows.Close() }()

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

// buildAuditFilteredQuery assembles the ListAuditFiltered query and its bound
// args for the newest n rows matching filter. Its index plan is a performance
// contract: with only an account restriction it reads an audit index in
// (at, id) order with no temporary sort; adding an exact account filter reads
// idx_audit_account_code.
func buildAuditFilteredQuery(
	dictionaries *enumDictionaries, filter domain.AuditFilter, n int,
) (string, []any, error) {
	args := make([]any, 0, 5)
	where := []string{"1 = 1"}
	appendAccountRestriction(
		&where, &args, auditRestrictionColumn, auditUnownedPredicate,
		filter.Restriction,
	)
	q := auditSelect + ` WHERE ` + strings.Join(where, ` AND `)
	if filter.Account != "" {
		q += ` AND au.account_code = ?`
		args = append(args, filter.Account.String())
	}
	if filter.Source != "" {
		sourceID, err := dictionaries.id(sourceKindTable, "source", string(filter.Source))
		if err != nil {
			return "", nil, err
		}
		q += ` AND au.source_id = ?`
		args = append(args, sourceID)
	}
	if len(filter.Actions) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(filter.Actions)), ",")
		q += fmt.Sprintf(` AND au.action_id IN (%s)`, placeholders)
		for _, action := range filter.Actions {
			id, err := dictionaries.id(auditActionTable, "audit action", string(action))
			if err != nil {
				return "", nil, err
			}
			args = append(args, id)
		}
	}
	q += ` ORDER BY au.at DESC, au.id DESC LIMIT ?`
	args = append(args, n)
	return q, args, nil
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
// the pre-built filter clauses. Its index plan is a performance contract: the
// browse views (no filter or the control category, with or without an account
// restriction) read an audit index in (at, id) order with no temporary sort.
func buildAuditListQuery(
	clauses []string,
	args []any,
	page fwstore.PageSpec,
) (string, []any) {
	queryArgs := append([]any{}, args...)
	query := `
SELECT au.external_id, au.account_code, au.account_title, au.asset_code,
       au.group_code, au.actor_code, au.actor_title, au.at, au.action_id,
       au.source_id, au.detail, au.detail_code, au.detail_params,
       au.detail_fields, au.order_id, au.verdict, au.reject_code
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
	appendAccountRestriction(
		&clauses, &args, auditRestrictionColumn, auditUnownedPredicate,
		filter.Restriction,
	)
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
		detail                                                 scannedAuditDetail
		orderID, verdict, rejectCode                           string
	)
	if err := rows.Scan(
		&extID, &account, &accountTitle, &asset, &group, &actor,
		&actorTitle, &at, &actionID, &sourceID, &detail.text, &detail.code,
		&detail.params, &detail.fields, &orderID, &verdict, &rejectCode,
	); err != nil {
		return domain.AuditRow{}, fmt.Errorf("store: scan audit row: %w", err)
	}
	row, err := auditRowFromScanned(
		dictionaries, extID, account, accountTitle, asset, group, actor, actorTitle,
		at, actionID, sourceID, detail, orderID, verdict, rejectCode,
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
		detail                                                 scannedAuditDetail
		orderID, verdict, rejectCode                           string
	)
	if err := rows.Scan(
		&extID, &account, &accountTitle, &asset, &group, &actor, &actorTitle,
		&at, &actionID, &sourceID, &detail.text, &detail.code, &detail.params,
		&detail.fields, &orderID, &verdict, &rejectCode,
	); err != nil {
		return domain.AuditRow{}, fmt.Errorf("store: scan audit: %w", err)
	}
	return auditRowFromScanned(
		dictionaries, extID, account, accountTitle, asset, group, actor, actorTitle,
		at, actionID, sourceID, detail, orderID, verdict, rejectCode,
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
	detail scannedAuditDetail,
	orderID, verdict, rejectCode string,
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
	decoded, err := detail.decode()
	if err != nil {
		return domain.AuditRow{}, fmt.Errorf("store: decode audit %s detail: %w", xid, err)
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
		Detail:       decoded,
		OrderID:      orderID, Verdict: verdict, RejectCode: rejectCode,
	}, nil
}

// encodeAuditDetail encodes a detail's parameters and tail as the
// detail_params object and the detail_fields array.
func encodeAuditDetail(detail domain.AuditDetail) (string, string, error) {
	paramsJSON, err := json.Marshal(detail.Params)
	if err != nil {
		return "", "", fmt.Errorf("store: encode audit detail params: %w", err)
	}
	fieldsJSON, err := json.Marshal(detail.Fields)
	if err != nil {
		return "", "", fmt.Errorf("store: encode audit detail fields: %w", err)
	}
	return string(paramsJSON), string(fieldsJSON), nil
}

// scannedAuditDetail holds the four detail columns of one audit row.
type scannedAuditDetail struct {
	text, code, params, fields string
}

// decode rebuilds the detail as written. An empty parameter object and an
// empty tail decode to nil, the way NewAuditDetail leaves a detail without
// them.
func (s scannedAuditDetail) decode() (domain.AuditDetail, error) {
	var params domain.AuditDetailParams
	if err := json.Unmarshal([]byte(s.params), &params); err != nil {
		return domain.AuditDetail{}, fmt.Errorf("params: %w", err)
	}
	if len(params) == 0 {
		params = nil
	}
	var fields domain.AuditDetailFields
	if err := json.Unmarshal([]byte(s.fields), &fields); err != nil {
		return domain.AuditDetail{}, fmt.Errorf("fields: %w", err)
	}
	if len(fields) == 0 {
		fields = nil
	}
	return domain.AuditDetail{
		Code:   domain.AuditDetailCode(s.code),
		Params: params,
		Fields: fields,
		Text:   s.text,
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
