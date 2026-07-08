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
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/businesscsv"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

// BusinessCSVExportRequest describes one business CSV export.
type BusinessCSVExportRequest struct {
	Entity    businesscsv.Entity
	Delimiter businesscsv.Delimiter
	Filter    businesscsv.ExportFilter
	Zip       bool
}

// BusinessCSVImportRequest describes one business CSV import.
type BusinessCSVImportRequest struct {
	Entity         businesscsv.Entity
	Delimiter      businesscsv.Delimiter
	ConflictPolicy businesscsv.ConflictPolicy
	Filename       string
	Payload        []byte
}

// BusinessCSVImportPreview reports conflicts without mutating state.
type BusinessCSVImportPreview struct {
	File      businesscsv.ImportFile   `json:"file"`
	Counts    businesscsv.ImportCounts `json:"counts"`
	Conflicts []businesscsv.Conflict   `json:"conflicts"`
}

// BusinessCSVImportResult reports the applied import outcome.
type BusinessCSVImportResult struct {
	File      businesscsv.ImportFile   `json:"file"`
	Counts    businesscsv.ImportCounts `json:"counts"`
	Conflicts []businesscsv.Conflict   `json:"conflicts"`
}

// ExportBusinessCSV exports one business entity as CSV or ZIP and records one
// operator audit event. It intentionally uses export-specific order/trade list
// paths so display limits cannot truncate downloaded files.
func (s *Service) ExportBusinessCSV(
	ctx context.Context, req BusinessCSVExportRequest,
) (businesscsv.ExportFile, error) {
	if err := businesscsv.ValidateEntity(req.Entity); err != nil {
		return businesscsv.ExportFile{}, err
	}
	if _, err := req.Delimiter.Rune(); err != nil {
		return businesscsv.ExportFile{}, err
	}

	var (
		body []byte
		err  error
	)
	switch req.Entity {
	case businesscsv.EntityAccountGroups:
		groups, lerr := s.businessCSVGroups(ctx)
		if lerr != nil {
			return businesscsv.ExportFile{}, lerr
		}
		body, err = businesscsv.EncodeGroups(groups, req.Delimiter)
	case businesscsv.EntityAccounts:
		accounts, lerr := s.businessCSVAccounts(ctx, req.Filter)
		if lerr != nil {
			return businesscsv.ExportFile{}, lerr
		}
		body, err = businesscsv.EncodeAccounts(accounts, req.Delimiter)
	case businesscsv.EntityPositions:
		balances, lerr := s.ListBalances(ctx, req.Filter.Account, req.Filter.Asset)
		if lerr != nil {
			return businesscsv.ExportFile{}, lerr
		}
		body, err = businesscsv.EncodePositions(balances, req.Delimiter)
	case businesscsv.EntityOrders:
		orders, lerr := s.ListAllOrders(ctx, req.Filter.Account, req.Filter.Source)
		if lerr != nil {
			return businesscsv.ExportFile{}, lerr
		}
		body, err = businesscsv.EncodeOrders(orders, req.Delimiter)
	case businesscsv.EntityTrades:
		trades, lerr := s.ListAllTrades(ctx, req.Filter.Account, req.Filter.Source)
		if lerr != nil {
			return businesscsv.ExportFile{}, lerr
		}
		body, err = businesscsv.EncodeTrades(trades, req.Delimiter)
	}
	if err != nil {
		return businesscsv.ExportFile{}, err
	}
	file, err := businesscsv.WrapExport(
		req.Entity, body, req.Zip, time.Now().UTC(),
	)
	if err != nil {
		return businesscsv.ExportFile{}, err
	}
	if err := s.auditBusinessCSV(ctx, domain.AuditActionExportBusinessCSV,
		businessCSVExportDetail(req)); err != nil {
		return businesscsv.ExportFile{}, err
	}
	return file, nil
}

func (s *Service) businessCSVGroups(ctx context.Context) ([]domain.AccountGroup, error) {
	groups, err := s.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	out := groups[:0]
	for _, group := range groups {
		if group.Code == "" {
			continue
		}
		out = append(out, group)
	}
	return out, nil
}

// parseBusinessCSVImport decodes the uploaded CSV/ZIP and parses its rows for the
// requested entity, validating the delimiter first.
func (s *Service) parseBusinessCSVImport(
	req BusinessCSVImportRequest,
) (businesscsv.ImportFile, businesscsv.ImportRows, error) {
	if _, err := req.Delimiter.Rune(); err != nil {
		return businesscsv.ImportFile{}, businesscsv.ImportRows{}, err
	}
	file, err := businesscsv.DecodeImportFile(req.Filename, req.Payload)
	if err != nil {
		return businesscsv.ImportFile{}, businesscsv.ImportRows{}, err
	}
	rows, err := businesscsv.ParseImport(req.Entity, file.Body, req.Delimiter)
	if err != nil {
		return businesscsv.ImportFile{}, businesscsv.ImportRows{}, err
	}
	return file, rows, nil
}

// PreviewBusinessCSVImport decodes the uploaded CSV/ZIP and reports conflicts.
func (s *Service) PreviewBusinessCSVImport(
	ctx context.Context, req BusinessCSVImportRequest,
) (BusinessCSVImportPreview, error) {
	file, rows, err := s.parseBusinessCSVImport(req)
	if err != nil {
		return BusinessCSVImportPreview{}, err
	}
	if err := s.validateBusinessCSVCurrencyAssets(ctx, req.Entity, rows); err != nil {
		return BusinessCSVImportPreview{}, err
	}
	conflicts, counts, err := s.businessCSVConflicts(ctx, req.Entity, rows)
	if err != nil {
		return BusinessCSVImportPreview{}, err
	}
	return BusinessCSVImportPreview{
		File:      file,
		Counts:    counts,
		Conflicts: conflicts,
	}, nil
}

// ImportBusinessCSV applies one business CSV import and records one CSV audit.
// The file attempt is audited whether the import succeeds or fails, including a
// parse failure: the business CSV parser now rejects malformed identity at parse
// time (e.g. an empty code), so the attempt audit is wired around both the parse
// and apply steps once the file decodes.
func (s *Service) ImportBusinessCSV(
	ctx context.Context, req BusinessCSVImportRequest,
) (BusinessCSVImportResult, error) {
	if err := businesscsv.ValidateConflictPolicy(req.ConflictPolicy); err != nil {
		return BusinessCSVImportResult{}, err
	}
	if _, err := req.Delimiter.Rune(); err != nil {
		return BusinessCSVImportResult{}, err
	}
	file, err := businesscsv.DecodeImportFile(req.Filename, req.Payload)
	if err != nil {
		return BusinessCSVImportResult{}, err
	}

	rows, err := businesscsv.ParseImport(req.Entity, file.Body, req.Delimiter)
	if err != nil {
		// A parse failure is still a file attempt: audit it with the data-row count
		// recovered from the raw CSV so the operator sees the rejected upload.
		counts := businesscsv.ImportCounts{Rows: businessCSVDataRowCount(file.Body, req.Delimiter)}
		return BusinessCSVImportResult{}, s.auditBusinessCSVImportError(ctx, req, file, counts, err)
	}
	if err := s.validateBusinessCSVCurrencyAssets(ctx, req.Entity, rows); err != nil {
		counts := businesscsv.ImportCounts{Rows: importRowCount(req.Entity, rows)}
		return BusinessCSVImportResult{}, s.auditBusinessCSVImportError(ctx, req, file, counts, err)
	}

	conflicts, counts, err := s.applyBusinessCSVImport(ctx, req.Entity, rows, req.ConflictPolicy)
	if err != nil {
		return BusinessCSVImportResult{}, s.auditBusinessCSVImportError(ctx, req, file, counts, err)
	}
	result := BusinessCSVImportResult{File: file, Counts: counts, Conflicts: conflicts}
	if err := s.auditBusinessCSV(ctx, domain.AuditActionImportBusinessCSV,
		businessCSVImportDetail(req, file, counts)); err != nil {
		return BusinessCSVImportResult{}, err
	}
	return result, nil
}

// auditBusinessCSVImportError records the failed-import attempt audit, joining any
// audit-write failure onto the original import error.
func (s *Service) auditBusinessCSVImportError(
	ctx context.Context,
	req BusinessCSVImportRequest,
	file businesscsv.ImportFile,
	counts businesscsv.ImportCounts,
	importErr error,
) error {
	if auditErr := s.auditBusinessCSV(ctx, domain.AuditActionImportBusinessCSV,
		businessCSVImportErrorDetail(req, file, counts, importErr)); auditErr != nil {
		return errors.Join(
			importErr,
			fmt.Errorf("audit failed business CSV import: %w", auditErr),
		)
	}
	return importErr
}

// businessCSVDataRowCount counts the non-header data rows in a decoded CSV body
// for the attempt audit on a parse failure (when no parsed counts exist yet). It
// is best-effort: a body that cannot be read as CSV yields zero.
func businessCSVDataRowCount(body []byte, delimiter businesscsv.Delimiter) int {
	comma, err := delimiter.Rune()
	if err != nil {
		return 0
	}
	r := csv.NewReader(bytes.NewReader(body))
	r.Comma = comma
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil || len(records) <= 1 {
		return 0
	}
	return len(records) - 1
}

func (s *Service) businessCSVAccounts(
	ctx context.Context, filter businesscsv.ExportFilter,
) ([]domain.Account, error) {
	accounts, err := s.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	if !filter.GroupCodeSet {
		return accounts, nil
	}
	filtered := make([]domain.Account, 0, len(accounts))
	for _, account := range accounts {
		if account.GroupCode == filter.GroupCode {
			filtered = append(filtered, account)
		}
	}
	return filtered, nil
}

func (s *Service) businessCSVConflicts(
	ctx context.Context, entity businesscsv.Entity, rows businesscsv.ImportRows,
) ([]businesscsv.Conflict, businesscsv.ImportCounts, error) {
	counts := businesscsv.ImportCounts{Rows: importRowCount(entity, rows)}
	existing, err := s.businessCSVExistingKeys(ctx, entity)
	if err != nil {
		return nil, counts, err
	}
	conflicts := make([]businesscsv.Conflict, 0)
	for _, candidate := range importKeys(entity, rows) {
		if existing[candidate.Key] {
			conflicts = append(conflicts, businesscsv.Conflict{
				Row: candidate.Row, Key: candidate.Key,
			})
		}
	}
	counts.Conflicts = len(conflicts)
	return conflicts, counts, nil
}

func (s *Service) applyBusinessCSVImport(
	ctx context.Context,
	entity businesscsv.Entity,
	rows businesscsv.ImportRows,
	policy businesscsv.ConflictPolicy,
) ([]businesscsv.Conflict, businesscsv.ImportCounts, error) {
	counts := businesscsv.ImportCounts{Rows: importRowCount(entity, rows)}
	existing, err := s.businessCSVExistingKeys(ctx, entity)
	if err != nil {
		return nil, counts, err
	}
	conflicts := make([]businesscsv.Conflict, 0)
	in := store.BusinessCSVImport{}
	selected := 0
	switch entity {
	case businesscsv.EntityAccountGroups:
		for i, row := range rows.Groups {
			if stop := s.handleImportConflict(
				row.Code, i+2, existing, policy, &counts, &conflicts,
			); stop {
				break
			}
			if existing[row.Code] && policy == businesscsv.ConflictSkip {
				continue
			}
			group, err := businessCSVGroupImport(row)
			if err != nil {
				return conflicts, counts, err
			}
			in.Groups = append(in.Groups, store.BusinessCSVImportGroup{
				Group:  group,
				Exists: existing[row.Code],
			})
			existing[row.Code] = true
			selected++
			if counts.Stopped {
				break
			}
		}
	case businesscsv.EntityAccounts:
		for i, row := range rows.Accounts {
			key := string(row.Code)
			if stop := s.handleImportConflict(
				key, i+2, existing, policy, &counts, &conflicts,
			); stop {
				break
			}
			if existing[key] && policy == businesscsv.ConflictSkip {
				continue
			}
			account, err := businessCSVAccountImport(row)
			if err != nil {
				return conflicts, counts, err
			}
			in.Accounts = append(in.Accounts, store.BusinessCSVImportAccount{
				Account: account,
				Exists:  existing[key],
			})
			existing[key] = true
			selected++
			if counts.Stopped {
				break
			}
		}
	case businesscsv.EntityPositions:
		for i, row := range rows.Positions {
			key := businessCSVPositionKey(row.Account, row.Asset)
			if stop := s.handleImportConflict(
				key, i+2, existing, policy, &counts, &conflicts,
			); stop {
				break
			}
			if existing[key] && policy == businesscsv.ConflictSkip {
				continue
			}
			balance, err := businessCSVPositionImport(row)
			if err != nil {
				return conflicts, counts, err
			}
			in.Balances = append(in.Balances, balance)
			existing[key] = true
			selected++
			if counts.Stopped {
				break
			}
		}
	default:
		return nil, counts, fmt.Errorf("business CSV entity %q is not importable: %w", entity, domain.ErrInvalid)
	}
	if selected == 0 {
		return conflicts, counts, nil
	}
	if err := s.applyBusinessCSVBatch(ctx, in); err != nil {
		return conflicts, counts, err
	}
	counts.Applied = selected
	return conflicts, counts, nil
}

func (s *Service) handleImportConflict(
	key string,
	row int,
	existing map[string]bool,
	policy businesscsv.ConflictPolicy,
	counts *businesscsv.ImportCounts,
	conflicts *[]businesscsv.Conflict,
) bool {
	if !existing[key] {
		return false
	}
	counts.Conflicts++
	*conflicts = append(*conflicts, businesscsv.Conflict{Row: row, Key: key})
	switch policy {
	case businesscsv.ConflictSkip:
		counts.Skipped++
		return false
	case businesscsv.ConflictStop:
		counts.Stopped = true
		return true
	default:
		return false
	}
}

func (s *Service) applyBusinessCSVBatch(
	ctx context.Context, in store.BusinessCSVImport,
) error {
	n, err := s.groupNode()
	if err != nil {
		return err
	}
	return n.ApplyBusinessCSVImport(ctx, in, auth.CallerFromContext(ctx))
}

func businessCSVGroupImport(row businesscsv.GroupRow) (domain.AccountGroup, error) {
	if err := domain.ValidateGroupID(row.Code); err != nil {
		return domain.AccountGroup{}, err
	}
	if err := domain.ValidateTitle(row.Title); err != nil {
		return domain.AccountGroup{}, err
	}
	if err := validateOptionalCurrency(row.Currency); err != nil {
		return domain.AccountGroup{}, err
	}
	if err := domain.ValidateNotes(row.Notes); err != nil {
		return domain.AccountGroup{}, err
	}
	return domain.AccountGroup{
		Code:        row.Code,
		Title:       row.Title,
		Currency:    row.Currency,
		Notes:       row.Notes,
		Blocked:     row.Blocked,
		BlockReason: row.BlockReason,
	}, nil
}

func businessCSVAccountImport(row businesscsv.AccountRow) (domain.Account, error) {
	if err := domain.ValidateAccountID(row.Code); err != nil {
		return domain.Account{}, err
	}
	if row.GroupCode != "" {
		if err := domain.ValidateGroupID(row.GroupCode); err != nil {
			return domain.Account{}, err
		}
	}
	if err := domain.ValidateNotes(row.Notes); err != nil {
		return domain.Account{}, err
	}
	if err := domain.ValidateTitle(row.Title); err != nil {
		return domain.Account{}, err
	}
	if err := validateOptionalCurrency(row.Currency); err != nil {
		return domain.Account{}, err
	}
	return domain.Account{
		Code:        row.Code,
		Title:       row.Title,
		GroupCode:   row.GroupCode,
		Currency:    row.Currency,
		Notes:       row.Notes,
		Blocked:     row.Blocked,
		BlockReason: row.BlockReason,
	}, nil
}

func businessCSVPositionImport(row businesscsv.PositionRow) (domain.Balance, error) {
	balance := domain.Balance{
		Account:           row.Account,
		Asset:             row.Asset,
		Available:         row.Available,
		Held:              row.Held,
		Incoming:          row.Incoming,
		RealizedPnl:       row.RealizedPnl,
		AverageEntryPrice: row.AverageEntryPrice,
	}
	if err := domain.ValidateAccountID(balance.Account); err != nil {
		return domain.Balance{}, err
	}
	if _, err := domain.AddDecimals("", balance.RealizedPnl); err != nil {
		return domain.Balance{}, err
	}
	return balance, nil
}

func (s *Service) businessCSVExistingKeys(
	ctx context.Context, entity businesscsv.Entity,
) (map[string]bool, error) {
	out := make(map[string]bool)
	switch entity {
	case businesscsv.EntityAccountGroups:
		groups, err := s.ListGroups(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range groups {
			out[row.Code] = true
		}
	case businesscsv.EntityAccounts:
		accounts, err := s.ListAccounts(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range accounts {
			out[string(row.Code)] = true
		}
	case businesscsv.EntityPositions:
		balances, err := s.ListBalances(ctx, "", "")
		if err != nil {
			return nil, err
		}
		for _, row := range balances {
			out[businessCSVPositionKey(row.Account, row.Asset)] = true
		}
	default:
		return nil, fmt.Errorf("business CSV entity %q is not importable: %w", entity, domain.ErrInvalid)
	}
	return out, nil
}

func importRowCount(entity businesscsv.Entity, rows businesscsv.ImportRows) int {
	switch entity {
	case businesscsv.EntityAccountGroups:
		return len(rows.Groups)
	case businesscsv.EntityAccounts:
		return len(rows.Accounts)
	case businesscsv.EntityPositions:
		return len(rows.Positions)
	default:
		return 0
	}
}

type importKey struct {
	Key string
	Row int
}

func importKeys(entity businesscsv.Entity, rows businesscsv.ImportRows) []importKey {
	out := make([]importKey, 0, importRowCount(entity, rows))
	switch entity {
	case businesscsv.EntityAccountGroups:
		for i, row := range rows.Groups {
			out = append(out, importKey{Row: i + 2, Key: row.Code})
		}
	case businesscsv.EntityAccounts:
		for i, row := range rows.Accounts {
			out = append(out, importKey{Row: i + 2, Key: string(row.Code)})
		}
	case businesscsv.EntityPositions:
		for i, row := range rows.Positions {
			out = append(out, importKey{
				Row: i + 2,
				Key: businessCSVPositionKey(row.Account, row.Asset),
			})
		}
	}
	return out
}

type businessCSVCurrencyRef struct {
	Code string
	Row  int
}

func businessCSVCurrencyRefs(
	entity businesscsv.Entity, rows businesscsv.ImportRows,
) []businessCSVCurrencyRef {
	out := make([]businessCSVCurrencyRef, 0)
	switch entity {
	case businesscsv.EntityAccountGroups:
		for i, row := range rows.Groups {
			if row.Currency != "" {
				out = append(out, businessCSVCurrencyRef{Row: i + 2, Code: row.Currency})
			}
		}
	case businesscsv.EntityAccounts:
		for i, row := range rows.Accounts {
			if row.Currency != "" {
				out = append(out, businessCSVCurrencyRef{Row: i + 2, Code: row.Currency})
			}
		}
	}
	return out
}

func (s *Service) validateBusinessCSVCurrencyAssets(
	ctx context.Context, entity businesscsv.Entity, rows businesscsv.ImportRows,
) error {
	refs := businessCSVCurrencyRefs(entity, rows)
	if len(refs) == 0 {
		return nil
	}
	assets, err := s.ListAssets(ctx)
	if err != nil {
		return fmt.Errorf("list assets for business CSV currencies: %w", err)
	}
	known := make(map[string]struct{}, len(assets))
	for _, asset := range assets {
		known[asset.Code] = struct{}{}
	}
	missing := make([]string, 0)
	seen := make(map[string]struct{})
	for _, ref := range refs {
		if _, ok := known[ref.Code]; ok {
			continue
		}
		if _, ok := seen[ref.Code]; ok {
			continue
		}
		missing = append(missing, fmt.Sprintf("row %d %s", ref.Row, ref.Code))
		seen[ref.Code] = struct{}{}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf(
		"business CSV references unknown currency asset(s) %s: %w",
		strings.Join(missing, ", "),
		domain.ErrInvalid,
	)
}

func businessCSVPositionKey(account domain.AccountID, asset string) string {
	return string(account) + "\x00" + asset
}

func (s *Service) auditBusinessCSV(
	ctx context.Context, action domain.AuditAction, detail string,
) error {
	n, err := s.groupNode()
	if err != nil {
		return err
	}
	return n.AppendAudit(ctx, store.AuditEntry{
		Action: action,
		Detail: detail,
	}, auth.CallerFromContext(ctx))
}

func businessCSVExportDetail(req BusinessCSVExportRequest) string {
	return fmt.Sprintf(
		"export business CSV entity=%s delimiter=%s zip=%t filters=%s",
		req.Entity, req.Delimiter, req.Zip, businessCSVFilterDetail(req.Filter),
	)
}

func businessCSVImportDetail(
	req BusinessCSVImportRequest,
	file businesscsv.ImportFile,
	counts businesscsv.ImportCounts,
) string {
	return fmt.Sprintf(
		"import business CSV entity=%s delimiter=%s file=%s type=%s policy=%s rows=%d applied=%d skipped=%d conflicts=%d stopped=%t",
		req.Entity, req.Delimiter, file.Name, file.Type, req.ConflictPolicy,
		counts.Rows, counts.Applied, counts.Skipped, counts.Conflicts, counts.Stopped,
	)
}

func businessCSVImportErrorDetail(
	req BusinessCSVImportRequest,
	file businesscsv.ImportFile,
	counts businesscsv.ImportCounts,
	err error,
) string {
	return businessCSVImportDetail(req, file, counts) + " error=" + err.Error()
}

func businessCSVFilterDetail(filter businesscsv.ExportFilter) string {
	parts := make([]string, 0, 4)
	if filter.GroupCodeSet {
		if filter.GroupCode == "" {
			parts = append(parts, "group=<none>")
		} else {
			parts = append(parts, "group="+filter.GroupCode)
		}
	}
	if filter.Account != "" {
		parts = append(parts, "account="+string(filter.Account))
	}
	if filter.Asset != "" {
		parts = append(parts, "asset="+filter.Asset)
	}
	if filter.Source != "" {
		parts = append(parts, "source="+string(filter.Source))
	}
	sort.Strings(parts)
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ",")
}

// ListAllOrders returns every order matching export filters, newest first.
func (s *Service) ListAllOrders(
	ctx context.Context, account domain.AccountID, source domain.Source,
) ([]domain.Order, error) {
	if account != "" {
		if err := domain.ValidateAccountID(account); err != nil {
			return nil, err
		}
	}
	orders := make([]domain.Order, 0)
	for i, target := range s.router.All() {
		part, err := target.ListAllOrders(ctx, account, source)
		if err != nil {
			return nil, fmt.Errorf("backend: node %d list all orders: %w", i, err)
		}
		orders = append(orders, part...)
	}
	sortOrdersNewestFirst(orders)
	return orders, nil
}

// ListAllTrades returns every trade matching export filters, newest first.
func (s *Service) ListAllTrades(
	ctx context.Context, account domain.AccountID, source domain.Source,
) ([]domain.Trade, error) {
	if account != "" {
		if err := domain.ValidateAccountID(account); err != nil {
			return nil, err
		}
	}
	trades := make([]domain.Trade, 0)
	for i, target := range s.router.All() {
		part, err := target.ListAllTrades(ctx, account, source)
		if err != nil {
			return nil, fmt.Errorf("backend: node %d list all trades: %w", i, err)
		}
		trades = append(trades, part...)
	}
	sortTradesNewestFirst(trades)
	return trades, nil
}
