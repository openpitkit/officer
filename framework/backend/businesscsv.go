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
	"log/slog"
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
		body, err = businesscsv.EncodeOrders(
			orders, req.Delimiter, s.businessCSVOrderLockPrice,
		)
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

func (s *Service) businessCSVOrderLockPrice(order domain.Order) string {
	if len(order.Lock) == 0 {
		return ""
	}
	price, err := s.lockSettlement(order.Lock, order)
	if err != nil {
		slog.Warn(
			"order CSV lock price unavailable",
			"order", order.ExternalID,
			"error", err,
		)
		return ""
	}
	return price
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

func (s *Service) auditBusinessCSV(
	ctx context.Context, action domain.AuditAction, detail string,
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

func businessCSVExportDetail(req BusinessCSVExportRequest) string {
	return fmt.Sprintf(
		"export business CSV entity=%s delimiter=%s zip=%t filters=%s",
		req.Entity, req.Delimiter, req.Zip, businessCSVFilterDetail(req.Filter),
	)
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
	orders, err := s.node.ListAllOrders(ctx, account, source)
	if err != nil {
		return nil, err
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
	trades, err := s.node.ListAllTrades(ctx, account, source)
	if err != nil {
		return nil, err
	}
	sortTradesNewestFirst(trades)
	return trades, nil
}
