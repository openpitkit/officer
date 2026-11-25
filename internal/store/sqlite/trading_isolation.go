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

package sqlite

import (
	"context"
	"database/sql"
	"slices"

	"go.openpit.dev/officer/framework/domain"
)

func (r *realmStore) validateTradingAccessModes(ctx context.Context, tx *sql.Tx, accounts []domain.AccountID) error {
	accounts = slices.Clone(accounts)
	slices.Sort(accounts)
	for _, account := range slices.Compact(accounts) {
		rawModes, err := scanStrings(tx.QueryContext(ctx,
			`SELECT DISTINCT c.mode FROM account a
			 JOIN trading_access access ON
			   access.account_id = a.id OR access.account_group_id = a.group_id
			 JOIN trading_connection c ON c.id = access.connection_id
			 WHERE a.code = ? ORDER BY c.mode`, account.String()))
		if err != nil {
			return err
		}
		modes := make([]domain.TradingMode, len(rawModes))
		for i, mode := range rawModes {
			modes[i] = domain.TradingMode(mode)
		}
		if err := domain.ValidateTradingAccessModes(account, modes); err != nil {
			return domain.Internal(err)
		}
	}
	return nil
}

func tradingGroupAccounts(ctx context.Context, tx *sql.Tx, groupID any) ([]domain.AccountID, error) {
	codes, err := scanStrings(tx.QueryContext(ctx,
		`SELECT code FROM account WHERE group_id = ?`, groupID))
	if err != nil {
		return nil, err
	}
	accounts := make([]domain.AccountID, len(codes))
	for i, code := range codes {
		accounts[i] = domain.AccountID(code)
	}
	return accounts, nil
}

func (rt *restoreTx) affectTradingAccounts(ctx context.Context, query string, args ...any) error {
	codes, err := scanStrings(rt.tx.QueryContext(ctx, query, args...))
	if err != nil {
		return err
	}
	for _, code := range codes {
		rt.isolationAccounts[domain.AccountID(code)] = true
	}
	return nil
}

func (rt *restoreTx) affectTradingGroup(ctx context.Context, group string) error {
	return rt.affectTradingAccounts(ctx,
		`SELECT a.code FROM account a JOIN account_group g ON g.id = a.group_id
		 WHERE g.code = ?`, group)
}

func (rt *restoreTx) affectTradingConnection(ctx context.Context, connection domain.ExternalID) error {
	return rt.affectTradingAccounts(ctx,
		`SELECT DISTINCT a.code FROM account a
		 JOIN trading_access access ON
		   access.account_id = a.id OR access.account_group_id = a.group_id
		 JOIN trading_connection c ON c.id = access.connection_id
		 WHERE c.external_id = ?`, connection.Bytes())
}
