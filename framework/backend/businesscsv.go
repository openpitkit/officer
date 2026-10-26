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

package backend

import (
	"context"
	"sort"
	"strconv"
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

// ExportBusinessCSV exports one business entity as CSV or ZIP and records one
// operator audit event. It intentionally uses export-specific order/trade list
// paths so display limits cannot truncate downloaded files. Every entity is
// read through a list the account scope narrows.
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
			return businesscsv.ExportFile{}, businessCSVFilterError(lerr)
		}
		body, err = businesscsv.EncodeAccounts(accounts, req.Delimiter)
	case businesscsv.EntityPositions:
		balances, lerr := s.ListBalances(ctx, req.Filter.Account, req.Filter.Asset)
		if lerr != nil {
			return businesscsv.ExportFile{}, businessCSVFilterError(lerr)
		}
		body, err = businesscsv.EncodePositions(balances, req.Delimiter)
	case businesscsv.EntityOrders:
		orders, lerr := s.ListAllOrders(ctx, req.Filter.Account, req.Filter.Source)
		if lerr != nil {
			return businesscsv.ExportFile{}, businessCSVFilterError(lerr)
		}
		body, err = businesscsv.EncodeOrders(
			orders, req.Delimiter, s.orderDisplayPrice,
		)
	case businesscsv.EntityTrades:
		trades, lerr := s.ListAllTrades(ctx, req.Filter.Account, req.Filter.Source)
		if lerr != nil {
			return businesscsv.ExportFile{}, businessCSVFilterError(lerr)
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

// businessCSVAccounts lists the exported accounts: those within the account
// scope's list restriction, narrowed to the filter's account and to its group
// when they are set. A named account is checked with the scope as the other
// entities' account filter is, and a named group is an account-group input
// checked with the scope; their refusals are returned unchanged.
func (s *Service) businessCSVAccounts(
	ctx context.Context, filter businesscsv.ExportFilter,
) ([]domain.Account, error) {
	if filter.Account != "" {
		if _, err := s.listRestriction(ctx, filter.Account); err != nil {
			return nil, err
		}
		if err := domain.ValidateAccountID(filter.Account); err != nil {
			return nil, err
		}
	}
	if filter.GroupCodeSet && filter.GroupCode != "" {
		caller, err := auth.CallerFromContext(ctx)
		if err != nil {
			return nil, err
		}
		if err := s.scope.Group(caller, filter.GroupCode); err != nil {
			return nil, err
		}
	}
	accounts, err := s.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	filtered := make([]domain.Account, 0, len(accounts))
	for _, account := range accounts {
		if filter.Account != "" && account.Code != filter.Account {
			continue
		}
		if filter.GroupCodeSet && account.GroupCode != filter.GroupCode {
			continue
		}
		filtered = append(filtered, account)
	}
	return filtered, nil
}

// businessCSVFilterError points a validation error an export filter caused at
// that filter in the request body. The lists the export reads take the filter
// as their own account or account-group input, so their errors name /account
// or /accountGroup; every other error is returned unchanged.
func businessCSVFilterError(err error) error {
	switch domain.ValidationPointer(err) {
	case "/account":
		return domain.WithValidationPointer("/filters/account", err)
	case "/accountGroup":
		return domain.WithValidationPointer("/filters/groupCode", err)
	default:
		return err
	}
}

func (s *Service) auditBusinessCSV(
	ctx context.Context, action domain.AuditAction, detail domain.AuditDetail,
) error {
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return err
	}
	return s.node.AppendAudit(ctx, store.AuditEntry{
		Action: action,
		Detail: detail,
	}, caller)
}

func businessCSVExportDetail(req BusinessCSVExportRequest) domain.AuditDetail {
	params := domain.AuditDetailParams{
		"entity":    string(req.Entity),
		"delimiter": string(req.Delimiter),
		"zip":       strconv.FormatBool(req.Zip),
	}
	filters := businessCSVFilterDetail(req.Filter)
	if filters == "" {
		return domain.NewAuditDetail(domain.AuditDetailExportBusinessCSVUnfiltered, params)
	}
	params["filters"] = filters
	return domain.NewAuditDetail(domain.AuditDetailExportBusinessCSV, params)
}

// businessCSVFilterDetail renders the set filters as sorted key=value pairs, or
// an empty string when no filter is set.
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
	return strings.Join(parts, ",")
}

// ListAllOrders returns every order matching export filters, newest first,
// within the account scope's list restriction, asked with that account.
func (s *Service) ListAllOrders(
	ctx context.Context, account domain.AccountID, source domain.Source,
) ([]domain.Order, error) {
	restriction, err := s.listRestriction(ctx, account)
	if err != nil {
		return nil, err
	}
	scoped, ok := restrictedAccount(account, restriction)
	if !ok {
		return []domain.Order{}, nil
	}
	if account != "" {
		if err := domain.ValidateAccountID(account); err != nil {
			return nil, err
		}
	}
	orders, err := s.node.ListAllOrders(ctx, scoped, source)
	if err != nil {
		return nil, err
	}
	sortOrdersNewestFirst(orders)
	return orders, nil
}

// ListAllTrades returns every trade matching export filters, newest first,
// within the account scope's list restriction, asked with that account.
func (s *Service) ListAllTrades(
	ctx context.Context, account domain.AccountID, source domain.Source,
) ([]domain.Trade, error) {
	restriction, err := s.listRestriction(ctx, account)
	if err != nil {
		return nil, err
	}
	scoped, ok := restrictedAccount(account, restriction)
	if !ok {
		return []domain.Trade{}, nil
	}
	if account != "" {
		if err := domain.ValidateAccountID(account); err != nil {
			return nil, err
		}
	}
	trades, err := s.node.ListAllTrades(ctx, scoped, source)
	if err != nil {
		return nil, err
	}
	sortTradesNewestFirst(trades)
	return trades, nil
}
