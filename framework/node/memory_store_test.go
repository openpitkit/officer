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
	"fmt"
	"math/big"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

var (
	_ store.Store      = (*memoryStore)(nil)
	_ store.RealmStore = (*memoryRealm)(nil)
)

type memoryStore struct {
	mu      sync.Mutex
	realm   *memoryRealm
	path    string
	version int
	closed  bool
}

type memoryRealm struct {
	store *memoryStore

	nextID uint64

	assets       map[string]domain.Asset
	assetClasses map[string]domain.AssetClass
	principals   map[string]domain.Principal
	groups       map[string]domain.AccountGroup
	accounts     map[domain.AccountID]domain.Account
	balances     map[string]domain.Balance

	rateLimits      map[string]domain.LimitRate
	orderSizeLimits map[string]domain.LimitOrderSize
	pnlBoundsLimits map[string]domain.LimitPnlBounds

	adjustments []domain.AccountAdjustmentRecord
	orders      map[domain.ExternalID]domain.Order
	approvals   map[domain.ExternalID]domain.OrderApproval
	events      []domain.OrderEvent
	trades      []domain.Trade
	audit       []domain.AuditRow

	instances   map[domain.ExternalID]domain.MarketDataInstance
	instruments map[string]domain.MarketDataInstrument
	quotes      map[string]domain.MarketDataQuote

	signingKeys   map[string]domain.SigningKey
	signingConfig map[string]string
	mcpAccess     map[string]bool
	userSettings  map[string]domain.UserSetting
	reservations  map[string]domain.ReservationIntent
	nextAccountID domain.EngineAccountID
	nextGroupID   int
}

func newMemoryStore(path string) *memoryStore {
	st := &memoryStore{path: path, version: 1}
	st.realm = newMemoryRealm(st)
	return st
}

func newMemoryRealm(st *memoryStore) *memoryRealm {
	return &memoryRealm{
		store:           st,
		assets:          map[string]domain.Asset{},
		assetClasses:    map[string]domain.AssetClass{},
		principals:      map[string]domain.Principal{},
		groups:          map[string]domain.AccountGroup{},
		accounts:        map[domain.AccountID]domain.Account{},
		balances:        map[string]domain.Balance{},
		rateLimits:      map[string]domain.LimitRate{},
		orderSizeLimits: map[string]domain.LimitOrderSize{},
		pnlBoundsLimits: map[string]domain.LimitPnlBounds{},
		orders:          map[domain.ExternalID]domain.Order{},
		approvals:       map[domain.ExternalID]domain.OrderApproval{},
		instances:       map[domain.ExternalID]domain.MarketDataInstance{},
		instruments:     map[string]domain.MarketDataInstrument{},
		quotes:          map[string]domain.MarketDataQuote{},
		signingKeys:     map[string]domain.SigningKey{},
		signingConfig:   map[string]string{},
		mcpAccess:       map[string]bool{},
		userSettings:    map[string]domain.UserSetting{},
		reservations:    map[string]domain.ReservationIntent{},
	}
}

func (s *memoryStore) ForRealm(
	_ context.Context, realm domain.RealmID,
) (store.RealmStore, error) {
	if realm != "" && realm != domain.DefaultRealm {
		return nil, fmt.Errorf("unknown realm %q: %w", realm, domain.ErrInvalid)
	}
	return s.realm, nil
}

func (s *memoryStore) Migrate(context.Context) error { return nil }

func (s *memoryStore) SchemaVersion(context.Context) (int, error) {
	return s.version, nil
}

func (s *memoryStore) Ping(context.Context) error { return nil }

func (s *memoryStore) Path() string { return s.path }

func (s *memoryStore) Reset(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.realm = newMemoryRealm(s)
	return nil
}

func (s *memoryStore) Close() error {
	s.closed = true
	return nil
}

func (r *memoryRealm) nextExternalID() domain.ExternalID {
	r.nextID++
	var id domain.ExternalID
	for i := 0; i < domain.ExternalIDByteLen; i++ {
		id[domain.ExternalIDByteLen-1-i] = byte(r.nextID >> (8 * i))
	}
	return id
}

func balanceKey(account domain.AccountID, asset string) string {
	return string(account) + "\x00" + asset
}

func limitKey(scope domain.LimitScope, account domain.AccountID, asset string) string {
	return string(scope) + "\x00" + string(account) + "\x00" + asset
}

func instrumentKey(instance domain.ExternalID, externalSymbol string) string {
	return instance.String() + "\x00" + externalSymbol
}

func settingKey(userID, key string) string { return userID + "\x00" + key }

func (r *memoryRealm) CreateAsset(_ context.Context, asset domain.Asset) error {
	if _, ok := r.assets[asset.Code]; ok {
		return domain.ErrAlreadyExists
	}
	r.assets[asset.Code] = asset
	return nil
}

func (r *memoryRealm) GetAsset(_ context.Context, code string) (domain.Asset, bool, error) {
	asset, ok := r.assets[code]
	return asset, ok, nil
}

func (r *memoryRealm) ListAssets(context.Context) ([]domain.Asset, error) {
	page, err := r.ListAssetRows(context.Background(), store.AssetListFilter{})
	if err != nil {
		return nil, err
	}
	return page.Rows, nil
}

func (r *memoryRealm) ListAssetRows(
	_ context.Context, filter store.AssetListFilter,
) (store.AssetListPage, error) {
	out := make([]domain.Asset, 0, len(r.assets))
	for _, asset := range r.assets {
		codeMatches := textMatches(filter.Code, asset.Code) ||
			textMatches(filter.Code, asset.Title)
		if !codeMatches ||
			!textMatches(filter.Class, asset.AssetClass) {
			continue
		}
		out = append(out, asset)
	}
	sort.Slice(out, func(i, j int) bool {
		leftTie := out[i].Code < out[j].Code
		var less bool
		switch filter.Sort.Column {
		case "assetClass":
			less = out[i].AssetClass < out[j].AssetClass ||
				(out[i].AssetClass == out[j].AssetClass && leftTie)
		case "title":
			less = out[i].Title < out[j].Title ||
				(out[i].Title == out[j].Title && leftTie)
		default:
			less = leftTie
		}
		if filter.Sort.Descending {
			switch filter.Sort.Column {
			case "assetClass":
				return out[i].AssetClass > out[j].AssetClass ||
					(out[i].AssetClass == out[j].AssetClass && out[i].Code > out[j].Code)
			case "title":
				return out[i].Title > out[j].Title ||
					(out[i].Title == out[j].Title && out[i].Code > out[j].Code)
			default:
				return out[i].Code > out[j].Code
			}
		}
		return less
	})
	total := len(out)
	if filter.Page.Limit > 0 {
		start := min(max(filter.Page.Offset, 0), len(out))
		end := min(start+filter.Page.Limit, len(out))
		out = out[start:end]
	}
	return store.AssetListPage{Rows: out, Total: total}, nil
}

func (r *memoryRealm) UpdateAsset(
	_ context.Context, oldCode string, asset domain.Asset,
) (domain.Asset, error) {
	if _, ok := r.assets[oldCode]; !ok {
		return domain.Asset{}, domain.ErrNotFound
	}
	if oldCode != asset.Code {
		if _, ok := r.assets[asset.Code]; ok {
			return domain.Asset{}, domain.ErrAlreadyExists
		}
		delete(r.assets, oldCode)
	}
	r.assets[asset.Code] = asset
	return asset, nil
}

func (r *memoryRealm) DeleteAsset(_ context.Context, code string, _ bool) error {
	if _, ok := r.assets[code]; !ok {
		return domain.ErrNotFound
	}
	delete(r.assets, code)
	return nil
}

func (r *memoryRealm) CreateAssetClass(_ context.Context, class domain.AssetClass) error {
	if _, ok := r.assetClasses[class.Code]; ok {
		return domain.ErrAlreadyExists
	}
	r.assetClasses[class.Code] = class
	return nil
}

func (r *memoryRealm) GetAssetClass(
	_ context.Context, code string,
) (domain.AssetClass, bool, error) {
	class, ok := r.assetClasses[code]
	return class, ok, nil
}

func (r *memoryRealm) ListAssetClasses(context.Context) ([]domain.AssetClass, error) {
	out := make([]domain.AssetClass, 0, len(r.assetClasses))
	for _, class := range r.assetClasses {
		out = append(out, class)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}

func (r *memoryRealm) ListAssetClassRows(
	ctx context.Context, filter store.AssetClassListFilter,
) (store.AssetClassListPage, error) {
	classes, err := r.ListAssetClasses(ctx)
	if err != nil {
		return store.AssetClassListPage{}, err
	}
	out := make([]store.AssetClassListRow, 0, len(classes))
	for _, class := range classes {
		row := store.AssetClassListRow{Class: class}
		for _, asset := range r.assets {
			if asset.AssetClass == class.Code {
				row.AssetCount++
			}
		}
		if !textMatchesAny(filter.Code, row.Class.Code, row.Class.Title) ||
			!textMatches(filter.Notes, row.Class.Notes) {
			continue
		}
		out = append(out, row)
	}
	sortAssetClassRows(out, filter.Sort)
	total := len(out)
	out = pageRows(out, filter.Page)
	return store.AssetClassListPage{Rows: out, Total: total}, nil
}

func (r *memoryRealm) UpdateAssetClass(
	_ context.Context, oldCode string, class domain.AssetClass,
) (domain.AssetClass, error) {
	if _, ok := r.assetClasses[oldCode]; !ok {
		return domain.AssetClass{}, domain.ErrNotFound
	}
	if oldCode != class.Code {
		if _, ok := r.assetClasses[class.Code]; ok {
			return domain.AssetClass{}, domain.ErrAlreadyExists
		}
		delete(r.assetClasses, oldCode)
		for code, asset := range r.assets {
			if asset.AssetClass == oldCode {
				asset.AssetClass = class.Code
				r.assets[code] = asset
			}
		}
	}
	r.assetClasses[class.Code] = class
	return class, nil
}

func (r *memoryRealm) DeleteAssetClass(_ context.Context, code string, force bool) error {
	if _, ok := r.assetClasses[code]; !ok {
		return domain.ErrNotFound
	}
	count := 0
	for _, asset := range r.assets {
		if asset.AssetClass == code {
			count++
		}
	}
	if count > 0 && !force {
		return domain.NewHasDependentsError([]domain.DependentCount{{Kind: "asset", Count: count}})
	}
	for c, asset := range r.assets {
		if asset.AssetClass == code {
			asset.AssetClass = ""
			r.assets[c] = asset
		}
	}
	delete(r.assetClasses, code)
	return nil
}

func (r *memoryRealm) CreatePrincipal(
	_ context.Context, principal domain.Principal,
) error {
	if _, ok := r.principals[principal.Code]; ok {
		return domain.ErrAlreadyExists
	}
	r.principals[principal.Code] = principal
	return nil
}

func (r *memoryRealm) GetPrincipal(
	_ context.Context, code string,
) (domain.Principal, bool, error) {
	principal, ok := r.principals[code]
	return principal, ok, nil
}

func (r *memoryRealm) ListPrincipals(context.Context) ([]domain.Principal, error) {
	out := make([]domain.Principal, 0, len(r.principals))
	for _, principal := range r.principals {
		out = append(out, principal)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}

func (r *memoryRealm) UpdatePrincipal(
	_ context.Context, principal domain.Principal,
) error {
	if _, ok := r.principals[principal.Code]; !ok {
		return domain.ErrNotFound
	}
	r.principals[principal.Code] = principal
	return nil
}

func (r *memoryRealm) DeletePrincipal(_ context.Context, code string) error {
	if _, ok := r.principals[code]; !ok {
		return domain.ErrNotFound
	}
	delete(r.principals, code)
	return nil
}

func (r *memoryRealm) CreateGroup(
	_ context.Context, group domain.AccountGroup,
) (domain.AccountGroup, error) {
	if _, ok := r.groups[group.Code]; ok {
		return domain.AccountGroup{}, domain.ErrAlreadyExists
	}
	r.nextGroupID++
	group.EngineGroupID = domain.EngineGroupID(r.nextGroupID)
	r.groups[group.Code] = group
	return group, nil
}

func (r *memoryRealm) GetGroup(
	_ context.Context, code string,
) (domain.AccountGroup, bool, error) {
	group, ok := r.groups[code]
	return group, ok, nil
}

func (r *memoryRealm) ListGroups(context.Context) ([]domain.AccountGroup, error) {
	out := make([]domain.AccountGroup, 0, len(r.groups))
	for _, group := range r.groups {
		out = append(out, group)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}

func (r *memoryRealm) ListGroupRows(
	ctx context.Context, _ store.GroupListFilter,
) (store.GroupListPage, error) {
	groups, err := r.ListGroups(ctx)
	if err != nil {
		return store.GroupListPage{}, err
	}
	out := make([]store.GroupListRow, 0, len(groups)+1)
	defaultRow := store.GroupListRow{
		Group: domain.AccountGroup{Code: ""},
	}
	for _, account := range r.accounts {
		if account.GroupCode != "" {
			continue
		}
		defaultRow.AccountCount++
		for _, balance := range r.balances {
			if balance.Account == account.Code {
				defaultRow.PositionCount++
			}
		}
	}
	out = append(out, defaultRow)
	for _, group := range groups {
		row := store.GroupListRow{Group: group}
		for _, account := range r.accounts {
			if account.GroupCode != group.Code {
				continue
			}
			row.AccountCount++
			for _, balance := range r.balances {
				if balance.Account == account.Code {
					row.PositionCount++
				}
			}
		}
		out = append(out, row)
	}
	return store.GroupListPage{Rows: out, Total: len(groups)}, nil
}

func (r *memoryRealm) SetGroupNotes(_ context.Context, code, notes string) error {
	group, ok := r.groups[code]
	if !ok {
		return domain.ErrNotFound
	}
	group.Notes = notes
	r.groups[code] = group
	return nil
}

func (r *memoryRealm) UpdateGroup(
	_ context.Context, oldCode string, group domain.AccountGroup,
) (domain.AccountGroup, error) {
	current, ok := r.groups[oldCode]
	if !ok {
		return domain.AccountGroup{}, domain.ErrNotFound
	}
	if oldCode != group.Code {
		if _, ok := r.groups[group.Code]; ok {
			return domain.AccountGroup{}, domain.ErrAlreadyExists
		}
		delete(r.groups, oldCode)
		for id, account := range r.accounts {
			if account.GroupCode == oldCode {
				account.GroupCode = group.Code
				r.accounts[id] = account
			}
		}
	}
	current.Code = group.Code
	current.Title = group.Title
	r.groups[group.Code] = current
	return current, nil
}

func (r *memoryRealm) SetGroupBlocked(
	_ context.Context, code string, blocked bool, reason string,
) error {
	group, ok := r.groups[code]
	if !ok {
		return domain.ErrNotFound
	}
	group.Blocked = blocked
	group.BlockReason = reason
	r.groups[code] = group
	return nil
}

func (r *memoryRealm) DeleteGroup(_ context.Context, code string) error {
	if _, ok := r.groups[code]; !ok {
		return domain.ErrNotFound
	}
	delete(r.groups, code)
	for id, account := range r.accounts {
		if account.GroupCode == code {
			account.GroupCode = ""
			r.accounts[id] = account
		}
	}
	return nil
}

func (r *memoryRealm) ListGroupAccounts(
	_ context.Context, code string,
) ([]domain.Account, error) {
	var out []domain.Account
	for _, account := range r.accounts {
		if account.GroupCode == code {
			out = append(out, account)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}

func (r *memoryRealm) CreateAccount(
	_ context.Context, account domain.Account,
) (domain.Account, error) {
	if _, ok := r.accounts[account.Code]; ok {
		return domain.Account{}, domain.ErrAlreadyExists
	}
	if account.GroupCode != "" {
		if _, ok := r.groups[account.GroupCode]; !ok {
			return domain.Account{}, domain.ErrInvalid
		}
	}
	r.nextAccountID++
	account.EngineAccountID = r.nextAccountID
	r.accounts[account.Code] = account
	return account, nil
}

func (r *memoryRealm) GetAccount(
	_ context.Context, code domain.AccountID,
) (domain.Account, bool, error) {
	account, ok := r.accounts[code]
	return account, ok, nil
}

func (r *memoryRealm) ListAccounts(context.Context) ([]domain.Account, error) {
	out := make([]domain.Account, 0, len(r.accounts))
	for _, account := range r.accounts {
		out = append(out, account)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}

func (r *memoryRealm) ListAccountRows(
	ctx context.Context, filter store.AccountListFilter,
) (store.AccountListPage, error) {
	accounts, err := r.ListAccounts(ctx)
	if err != nil {
		return store.AccountListPage{}, err
	}
	out := make([]store.AccountListRow, 0, len(accounts))
	for _, account := range accounts {
		row := store.AccountListRow{Account: account}
		for _, balance := range r.balances {
			if balance.Account == account.Code {
				row.PositionCount++
			}
		}
		if !textMatchesAny(filter.Code, string(row.Account.Code), row.Account.Title) ||
			!textMatches(filter.BlockReason, row.Account.BlockReason) ||
			!statusMatches(filter.Status, row.Account.Blocked) ||
			!countMatches(filter.Position, row.PositionCount) {
			continue
		}
		if filter.GroupCode != nil && row.Account.GroupCode != *filter.GroupCode {
			continue
		}
		out = append(out, row)
	}
	sortAccountRows(out, filter.Sort)
	total := len(out)
	out = pageRows(out, filter.Page)
	return store.AccountListPage{Rows: out, Total: total}, nil
}

func (r *memoryRealm) SetAccountBlocked(
	_ context.Context, code domain.AccountID, blocked bool, reason string,
) error {
	account, ok := r.accounts[code]
	if !ok {
		return domain.ErrNotFound
	}
	account.Blocked = blocked
	account.BlockReason = reason
	r.accounts[code] = account
	return nil
}

func (r *memoryRealm) SetAccountGroup(
	_ context.Context, code domain.AccountID, groupCode string,
) error {
	account, ok := r.accounts[code]
	if !ok {
		return domain.ErrNotFound
	}
	if groupCode != "" {
		if _, ok := r.groups[groupCode]; !ok {
			return domain.ErrInvalid
		}
	}
	account.GroupCode = groupCode
	r.accounts[code] = account
	return nil
}

func (r *memoryRealm) SetAccountNotes(
	_ context.Context, code domain.AccountID, notes string,
) error {
	account, ok := r.accounts[code]
	if !ok {
		return domain.ErrNotFound
	}
	account.Notes = notes
	r.accounts[code] = account
	return nil
}

func (r *memoryRealm) UpdateAccount(
	_ context.Context, oldCode domain.AccountID, account domain.Account,
) (domain.Account, error) {
	current, ok := r.accounts[oldCode]
	if !ok {
		return domain.Account{}, domain.ErrNotFound
	}
	if oldCode != account.Code {
		if _, ok := r.accounts[account.Code]; ok {
			return domain.Account{}, domain.ErrAlreadyExists
		}
		delete(r.accounts, oldCode)
		r.renameAccountReferences(oldCode, account.Code)
	}
	current.Code = account.Code
	current.Title = account.Title
	r.accounts[account.Code] = current
	return current, nil
}

func (r *memoryRealm) renameAccountReferences(oldCode, newCode domain.AccountID) {
	nextBalances := map[string]domain.Balance{}
	for key, balance := range r.balances {
		if balance.Account == oldCode {
			balance.Account = newCode
			key = balanceKey(newCode, balance.Asset)
		}
		nextBalances[key] = balance
	}
	r.balances = nextBalances

	r.rateLimits = renameAccountLimits(r.rateLimits, oldCode, newCode)
	r.orderSizeLimits = renameAccountLimits(r.orderSizeLimits, oldCode, newCode)
	r.pnlBoundsLimits = renameAccountLimits(r.pnlBoundsLimits, oldCode, newCode)

	for i := range r.adjustments {
		if r.adjustments[i].Account == oldCode {
			r.adjustments[i].Account = newCode
		}
	}
	for id, order := range r.orders {
		if order.Account == oldCode {
			order.Account = newCode
			r.orders[id] = order
		}
	}
	for i := range r.trades {
		if r.trades[i].Account == oldCode {
			r.trades[i].Account = newCode
		}
	}
}

func renameAccountLimits[Limit interface {
	domain.LimitRate | domain.LimitOrderSize | domain.LimitPnlBounds
}](
	limits map[string]Limit,
	oldCode domain.AccountID,
	newCode domain.AccountID,
) map[string]Limit {
	out := map[string]Limit{}
	for key, limit := range limits {
		switch typed := any(limit).(type) {
		case domain.LimitRate:
			if typed.Account == oldCode {
				typed.Account = newCode
				key = limitKey(typed.Scope, typed.Account, typed.Asset)
			}
			out[key] = any(typed).(Limit)
		case domain.LimitOrderSize:
			if typed.Account == oldCode {
				typed.Account = newCode
				key = limitKey(typed.Scope, typed.Account, typed.Asset)
			}
			out[key] = any(typed).(Limit)
		case domain.LimitPnlBounds:
			if typed.Account == oldCode {
				typed.Account = newCode
				key = limitKey(typed.Scope, typed.Account, typed.Asset)
			}
			out[key] = any(typed).(Limit)
		}
	}
	return out
}

func (r *memoryRealm) DeleteAccount(
	_ context.Context, code domain.AccountID, _ bool,
) error {
	if _, ok := r.accounts[code]; !ok {
		return domain.ErrNotFound
	}
	delete(r.accounts, code)
	return nil
}

func (r *memoryRealm) UpsertBalance(_ context.Context, balance domain.Balance) error {
	r.balances[balanceKey(balance.Account, balance.Asset)] = balance
	return nil
}

func (r *memoryRealm) GetBalance(
	_ context.Context, account domain.AccountID, asset string,
) (domain.Balance, bool, error) {
	balance, ok := r.balances[balanceKey(account, asset)]
	return balance, ok, nil
}

func (r *memoryRealm) ListBalances(
	_ context.Context, account domain.AccountID, asset string,
) ([]domain.Balance, error) {
	var out []domain.Balance
	for _, balance := range r.balances {
		if account != "" && balance.Account != account {
			continue
		}
		if asset != "" && balance.Asset != asset {
			continue
		}
		out = append(out, balance)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Account == out[j].Account {
			return out[i].Asset < out[j].Asset
		}
		return out[i].Account < out[j].Account
	})
	return out, nil
}

func (r *memoryRealm) ListBalanceRows(
	ctx context.Context, filter store.BalanceListFilter,
) (store.BalanceListPage, error) {
	balances, err := r.ListBalances(ctx, "", "")
	if err != nil {
		return store.BalanceListPage{}, err
	}
	rows := make([]store.BalanceListRow, 0, len(balances))
	for _, balance := range balances {
		row := store.BalanceListRow{Balance: balance}
		if !textMatches(filter.Account, string(balance.Account)) ||
			!textMatches(filter.Asset, balance.Asset) ||
			!decimalMatches(filter.Available, balance.Available) ||
			!decimalMatches(filter.Held, balance.Held) ||
			!decimalMatches(filter.Incoming, balance.Incoming) ||
			!decimalMatches(filter.AverageEntryPrice, balance.AverageEntryPrice) ||
			!decimalMatches(filter.RealizedPnl, balance.RealizedPnl) ||
			!timeMatches(filter.UpdatedAt, balance.UpdatedAt) {
			continue
		}
		if filter.GroupCode != nil {
			account, ok := r.accounts[balance.Account]
			if !ok || account.GroupCode != *filter.GroupCode {
				continue
			}
		}
		rows = append(rows, row)
	}
	sortBalanceRows(rows, filter.Sort)
	total := len(rows)
	rows = pageRows(rows, filter.Page)
	return store.BalanceListPage{Rows: rows, Total: total}, nil
}

func (r *memoryRealm) DeleteBalance(
	_ context.Context, account domain.AccountID, asset string,
) error {
	key := balanceKey(account, asset)
	if _, ok := r.balances[key]; !ok {
		return domain.ErrNotFound
	}
	delete(r.balances, key)
	return nil
}

func (r *memoryRealm) ListPolicyRows(
	_ context.Context, filter store.PolicyListFilter,
) (store.PolicyListPage, error) {
	wantKind := func(kind store.PolicyKind) bool {
		return filter.Kind == nil || *filter.Kind == kind
	}
	out := make([]store.PolicyListRow, 0)
	if wantKind(store.PolicyKindRate) {
		for _, limit := range r.rateLimits {
			if !textMatches(filter.Account, string(limit.Account)) ||
				!textMatches(filter.Asset, limit.Asset) {
				continue
			}
			value := limit
			out = append(out, store.PolicyListRow{
				Kind:    store.PolicyKindRate,
				Scope:   limit.Scope,
				Account: limit.Account,
				Asset:   limit.Asset,
				Rate:    &value,
			})
		}
	}
	if wantKind(store.PolicyKindOrderSize) {
		for _, limit := range r.orderSizeLimits {
			if !textMatches(filter.Account, string(limit.Account)) ||
				!textMatches(filter.Asset, limit.Asset) {
				continue
			}
			value := limit
			out = append(out, store.PolicyListRow{
				Kind:      store.PolicyKindOrderSize,
				Scope:     limit.Scope,
				Account:   limit.Account,
				Asset:     limit.Asset,
				OrderSize: &value,
			})
		}
	}
	if wantKind(store.PolicyKindPnlBounds) {
		for _, limit := range r.pnlBoundsLimits {
			if !textMatches(filter.Account, string(limit.Account)) ||
				!textMatches(filter.Asset, limit.Asset) {
				continue
			}
			value := limit
			out = append(out, store.PolicyListRow{
				Kind:      store.PolicyKindPnlBounds,
				Scope:     limit.Scope,
				Account:   limit.Account,
				Asset:     limit.Asset,
				PnlBounds: &value,
			})
		}
	}
	sortPolicyRows(out, filter.Sort)
	total := len(out)
	out = pageRows(out, filter.Page)
	return store.PolicyListPage{Rows: out, Total: total}, nil
}

// policyRowKey returns the deterministic composite sort key matching the
// connector's default ORDER BY (kind, scope, account, asset).
func policyRowKey(row store.PolicyListRow) string {
	return string(row.Kind) + "\x00" + row.Scope + "\x00" +
		string(row.Account) + "\x00" + row.Asset
}

func pageRows[T any](rows []T, page store.PageSpec) []T {
	if page.Limit <= 0 {
		return rows
	}
	start := min(max(page.Offset, 0), len(rows))
	end := min(start+page.Limit, len(rows))
	return rows[start:end]
}

func statusMatches(filter store.StatusFilter, blocked bool) bool {
	switch filter {
	case store.StatusFilterActive:
		return !blocked
	case store.StatusFilterBlocked:
		return blocked
	default:
		return true
	}
}

func countMatches(filter store.CountRangeFilter, value int) bool {
	if filter.Empty() {
		return true
	}
	if filter.Equal != nil && value != *filter.Equal {
		return false
	}
	if filter.NotEqual != nil && value == *filter.NotEqual {
		return false
	}
	if filter.Min != nil {
		if filter.MinExclusive && value <= *filter.Min {
			return false
		}
		if !filter.MinExclusive && value < *filter.Min {
			return false
		}
	}
	if filter.Max != nil {
		if filter.MaxExclusive && value >= *filter.Max {
			return false
		}
		if !filter.MaxExclusive && value > *filter.Max {
			return false
		}
	}
	return true
}

func decimalMatches(filter store.DecimalRangeFilter, value string) bool {
	if filter.Empty() {
		return true
	}
	if filter.Equal != nil && compareDecimalText(value, *filter.Equal) != 0 {
		return false
	}
	if filter.NotEqual != nil && compareDecimalText(value, *filter.NotEqual) == 0 {
		return false
	}
	if filter.Min != nil {
		cmp := compareDecimalText(value, *filter.Min)
		if filter.MinExclusive && cmp <= 0 {
			return false
		}
		if !filter.MinExclusive && cmp < 0 {
			return false
		}
	}
	if filter.Max != nil {
		cmp := compareDecimalText(value, *filter.Max)
		if filter.MaxExclusive && cmp >= 0 {
			return false
		}
		if !filter.MaxExclusive && cmp > 0 {
			return false
		}
	}
	return true
}

func timeMatches(filter store.TimeRangeFilter, value time.Time) bool {
	if filter.Empty() {
		return true
	}
	if filter.Min != nil {
		if filter.MinExclusive && !value.After(*filter.Min) {
			return false
		}
		if !filter.MinExclusive && value.Before(*filter.Min) {
			return false
		}
	}
	if filter.Max != nil {
		if filter.MaxExclusive && !value.Before(*filter.Max) {
			return false
		}
		if !filter.MaxExclusive && value.After(*filter.Max) {
			return false
		}
	}
	return true
}

func compareDecimalText(left, right string) int {
	leftDecimal, leftOK := new(big.Rat).SetString(left)
	rightDecimal, rightOK := new(big.Rat).SetString(right)
	if leftOK && rightOK {
		return leftDecimal.Cmp(rightDecimal)
	}
	return strings.Compare(left, right)
}

func compareStrings(left, right string) int {
	return strings.Compare(left, right)
}

func compareInts(left, right int) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

func compareBools(left, right bool) int {
	switch {
	case left == right:
		return 0
	case !left:
		return -1
	default:
		return 1
	}
}

func compareTimes(left, right time.Time) int {
	switch {
	case left.Before(right):
		return -1
	case left.After(right):
		return 1
	default:
		return 0
	}
}

func sortCompare(cmp int, descending bool) bool {
	if descending {
		return cmp > 0
	}
	return cmp < 0
}

func sortAccountRows(rows []store.AccountListRow, spec store.SortSpec) {
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		cmp := 0
		switch spec.Column {
		case "blockReason":
			cmp = compareStrings(left.Account.BlockReason, right.Account.BlockReason)
		case "group":
			cmp = compareStrings(left.Account.GroupCode, right.Account.GroupCode)
		case "positionCount":
			cmp = compareInts(left.PositionCount, right.PositionCount)
		case "status":
			cmp = compareBools(left.Account.Blocked, right.Account.Blocked)
		case "title":
			cmp = compareStrings(left.Account.Title, right.Account.Title)
		default:
			cmp = compareStrings(string(left.Account.Code), string(right.Account.Code))
		}
		if cmp == 0 {
			cmp = compareStrings(string(left.Account.Code), string(right.Account.Code))
		}
		return sortCompare(cmp, spec.Descending)
	})
}

func sortBalanceRows(rows []store.BalanceListRow, spec store.SortSpec) {
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i].Balance, rows[j].Balance
		cmp := 0
		switch spec.Column {
		case "asset":
			cmp = compareStrings(left.Asset, right.Asset)
		case "available":
			cmp = compareDecimalText(left.Available, right.Available)
		case "held":
			cmp = compareDecimalText(left.Held, right.Held)
		case "incoming":
			cmp = compareDecimalText(left.Incoming, right.Incoming)
		case "averageEntryPrice":
			cmp = compareDecimalText(left.AverageEntryPrice, right.AverageEntryPrice)
		case "realizedPnl":
			cmp = compareDecimalText(left.RealizedPnl, right.RealizedPnl)
		case "updatedAt":
			cmp = compareTimes(left.UpdatedAt, right.UpdatedAt)
		default:
			cmp = compareStrings(string(left.Account), string(right.Account))
		}
		if cmp == 0 {
			cmp = compareStrings(string(left.Account), string(right.Account))
		}
		if cmp == 0 {
			cmp = compareStrings(left.Asset, right.Asset)
		}
		return sortCompare(cmp, spec.Descending)
	})
}

func sortAssetClassRows(rows []store.AssetClassListRow, spec store.SortSpec) {
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		cmp := 0
		switch spec.Column {
		case "assetCount":
			cmp = compareInts(left.AssetCount, right.AssetCount)
		case "title":
			cmp = compareStrings(left.Class.Title, right.Class.Title)
		default:
			cmp = compareStrings(left.Class.Code, right.Class.Code)
		}
		if cmp == 0 {
			cmp = compareStrings(left.Class.Code, right.Class.Code)
		}
		return sortCompare(cmp, spec.Descending)
	})
}

func sortPolicyRows(rows []store.PolicyListRow, spec store.SortSpec) {
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		cmp := 0
		switch spec.Column {
		case "account":
			cmp = compareStrings(string(left.Account), string(right.Account))
		case "asset":
			cmp = compareStrings(left.Asset, right.Asset)
		case "initialPnl":
			cmp = compareDecimalText(policyInitialPnl(left), policyInitialPnl(right))
		case "lowerBound":
			cmp = compareDecimalText(policyLowerBound(left), policyLowerBound(right))
		case "maxNotional":
			cmp = compareDecimalText(policyMaxNotional(left), policyMaxNotional(right))
		case "maxOrders":
			cmp = compareDecimalText(policyMaxOrders(left), policyMaxOrders(right))
		case "maxQuantity":
			cmp = compareDecimalText(policyMaxQuantity(left), policyMaxQuantity(right))
		case "scope":
			cmp = compareStrings(left.Scope, right.Scope)
		case "upperBound":
			cmp = compareDecimalText(policyUpperBound(left), policyUpperBound(right))
		default:
			cmp = compareStrings(string(left.Kind), string(right.Kind))
		}
		if cmp == 0 {
			cmp = compareStrings(policyRowKey(left), policyRowKey(right))
		}
		return sortCompare(cmp, spec.Descending)
	})
}

func policyMaxOrders(row store.PolicyListRow) string {
	if row.Rate == nil {
		return ""
	}
	return fmt.Sprintf("%d", row.Rate.MaxOrders)
}

func policyMaxQuantity(row store.PolicyListRow) string {
	if row.OrderSize == nil {
		return ""
	}
	return row.OrderSize.MaxQuantity
}

func policyMaxNotional(row store.PolicyListRow) string {
	if row.OrderSize == nil {
		return ""
	}
	return row.OrderSize.MaxNotional
}

func policyLowerBound(row store.PolicyListRow) string {
	if row.PnlBounds == nil {
		return ""
	}
	return row.PnlBounds.LowerBound
}

func policyUpperBound(row store.PolicyListRow) string {
	if row.PnlBounds == nil {
		return ""
	}
	return row.PnlBounds.UpperBound
}

func policyInitialPnl(row store.PolicyListRow) string {
	if row.PnlBounds == nil {
		return ""
	}
	return row.PnlBounds.InitialPnl
}

func (r *memoryRealm) ListRateLimits(
	_ context.Context, account domain.AccountID,
) ([]domain.LimitRate, error) {
	var out []domain.LimitRate
	for _, limit := range r.rateLimits {
		if account == "" || limit.Account == account {
			out = append(out, limit)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return limitKey(out[i].Scope, out[i].Account, out[i].Asset) <
			limitKey(out[j].Scope, out[j].Account, out[j].Asset)
	})
	return out, nil
}

func (r *memoryRealm) PutRateLimit(_ context.Context, limit domain.LimitRate) error {
	r.rateLimits[limitKey(limit.Scope, limit.Account, limit.Asset)] = limit
	return nil
}

func (r *memoryRealm) DeleteRateLimit(
	_ context.Context, scope domain.LimitScope, account domain.AccountID, asset string,
) error {
	key := limitKey(scope, account, asset)
	if _, ok := r.rateLimits[key]; !ok {
		return domain.ErrNotFound
	}
	delete(r.rateLimits, key)
	return nil
}

func (r *memoryRealm) ListOrderSizeLimits(
	_ context.Context, account domain.AccountID,
) ([]domain.LimitOrderSize, error) {
	var out []domain.LimitOrderSize
	for _, limit := range r.orderSizeLimits {
		if account == "" || limit.Account == account {
			out = append(out, limit)
		}
	}
	return out, nil
}

func (r *memoryRealm) PutOrderSizeLimit(
	_ context.Context, limit domain.LimitOrderSize,
) error {
	r.orderSizeLimits[limitKey(limit.Scope, limit.Account, limit.Asset)] = limit
	return nil
}

func (r *memoryRealm) DeleteOrderSizeLimit(
	_ context.Context, scope domain.LimitScope, account domain.AccountID, asset string,
) error {
	key := limitKey(scope, account, asset)
	if _, ok := r.orderSizeLimits[key]; !ok {
		return domain.ErrNotFound
	}
	delete(r.orderSizeLimits, key)
	return nil
}

func (r *memoryRealm) ListPnlBoundsLimits(
	_ context.Context, account domain.AccountID,
) ([]domain.LimitPnlBounds, error) {
	var out []domain.LimitPnlBounds
	for _, limit := range r.pnlBoundsLimits {
		if account == "" || limit.Account == account {
			out = append(out, limit)
		}
	}
	return out, nil
}

func (r *memoryRealm) PutPnlBoundsLimit(
	_ context.Context, limit domain.LimitPnlBounds,
) error {
	r.pnlBoundsLimits[limitKey(limit.Scope, limit.Account, limit.Asset)] = limit
	return nil
}

func (r *memoryRealm) DeletePnlBoundsLimit(
	_ context.Context, scope domain.LimitScope, account domain.AccountID, asset string,
) error {
	key := limitKey(scope, account, asset)
	if _, ok := r.pnlBoundsLimits[key]; !ok {
		return domain.ErrNotFound
	}
	delete(r.pnlBoundsLimits, key)
	return nil
}

func (r *memoryRealm) AppendAdjustment(
	_ context.Context, rec domain.AccountAdjustmentRecord,
) (domain.AccountAdjustmentRecord, error) {
	if rec.ExternalID.IsZero() {
		rec.ExternalID = r.nextExternalID()
	} else {
		for _, existing := range r.adjustments {
			if existing.ExternalID == rec.ExternalID {
				return domain.AccountAdjustmentRecord{}, domain.ErrAlreadyExists
			}
		}
	}
	if rec.At.IsZero() {
		rec.At = time.Now().UTC()
	}
	r.adjustments = append(r.adjustments, rec)
	return rec, nil
}

func (r *memoryRealm) ListAdjustments(
	_ context.Context, account domain.AccountID, source domain.Source, n int,
) ([]domain.AccountAdjustmentRecord, error) {
	if n <= 0 {
		return []domain.AccountAdjustmentRecord{}, nil
	}
	var out []domain.AccountAdjustmentRecord
	for i := len(r.adjustments) - 1; i >= 0 && len(out) < n; i-- {
		rec := r.adjustments[i]
		if account != "" && rec.Account != account {
			continue
		}
		if source != "" && rec.Source != source {
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}

func (r *memoryRealm) ListAdjustmentRows(
	ctx context.Context, filter store.AdjustmentListFilter,
) (store.AdjustmentListPage, error) {
	records, err := r.ListAdjustments(ctx, "", "", len(r.adjustments))
	if err != nil {
		return store.AdjustmentListPage{}, err
	}
	out := make([]domain.AccountAdjustmentRecord, 0, len(records))
	for _, rec := range records {
		if !textMatches(filter.Account, rec.Account.String()) ||
			!textMatches(filter.Asset, rec.Asset) {
			continue
		}
		if filter.Source != "" && rec.Source != filter.Source {
			continue
		}
		if filter.Status != nil && adjustmentStatus(rec) != *filter.Status {
			continue
		}
		out = append(out, rec)
	}
	total := len(out)
	if filter.Page.Limit > 0 {
		start := min(max(filter.Page.Offset, 0), len(out))
		end := min(start+filter.Page.Limit, len(out))
		out = out[start:end]
	}
	return store.AdjustmentListPage{Rows: out, Total: total}, nil
}

func (r *memoryRealm) CreateOrder(
	_ context.Context, order domain.Order,
) (domain.Order, error) {
	if order.ExternalID.IsZero() {
		order.ExternalID = r.nextExternalID()
	} else if _, ok := r.orders[order.ExternalID]; ok {
		return domain.Order{}, domain.ErrAlreadyExists
	}
	if order.At.IsZero() {
		order.At = time.Now().UTC()
	}
	r.orders[order.ExternalID] = order
	return order, nil
}

func (r *memoryRealm) UpdateOrderStatus(
	_ context.Context, id domain.ExternalID, status domain.OrderStatus,
) error {
	order, ok := r.orders[id]
	if !ok {
		return domain.ErrNotFound
	}
	order.Status = status
	r.orders[id] = order
	return nil
}

func (r *memoryRealm) SetOrderLock(
	_ context.Context, id domain.ExternalID, lock []byte,
) error {
	order, ok := r.orders[id]
	if !ok {
		return domain.ErrNotFound
	}
	order.Lock = append([]byte(nil), lock...)
	r.orders[id] = order
	return nil
}

func (r *memoryRealm) PutOrderApproval(
	_ context.Context, id domain.ExternalID, env domain.OrderApproval,
) error {
	if _, ok := r.orders[id]; !ok {
		return nil
	}
	if _, ok := r.approvals[id]; !ok {
		r.approvals[id] = env
	}
	return nil
}

func (r *memoryRealm) GetOrder(
	_ context.Context, id domain.ExternalID,
) (domain.OrderDetail, error) {
	order, ok := r.orders[id]
	if !ok {
		return domain.OrderDetail{}, domain.ErrNotFound
	}
	detail := domain.OrderDetail{Order: order}
	if approval, ok := r.approvals[id]; ok {
		detail.Approval = &approval
	}
	for _, event := range r.events {
		if event.Order == id {
			detail.Events = append(detail.Events, event)
		}
	}
	for _, trade := range r.trades {
		if trade.Order == id {
			detail.Trades = append(detail.Trades, trade)
		}
	}
	return detail, nil
}

func (r *memoryRealm) ListOrders(
	ctx context.Context, account domain.AccountID, source domain.Source, n int,
) ([]domain.Order, error) {
	if n <= 0 {
		return []domain.Order{}, nil
	}
	all, err := r.ListAllOrders(ctx, account, source)
	if err != nil || len(all) <= n {
		return all, err
	}
	return all[:n], nil
}

func (r *memoryRealm) ListOrderRows(
	ctx context.Context, filter store.OrderListFilter,
) (store.OrderListPage, error) {
	orders, err := r.ListAllOrders(ctx, filter.Account, filter.Source)
	if err != nil {
		return store.OrderListPage{}, err
	}
	rows := make([]store.OrderListRow, 0, len(orders))
	for _, order := range orders {
		rows = append(rows, store.OrderListRow{Order: order})
	}
	if filter.Page.Limit > 0 {
		start := min(max(filter.Page.Offset, 0), len(rows))
		end := min(start+filter.Page.Limit, len(rows))
		rows = rows[start:end]
	}
	return store.OrderListPage{Rows: rows, Total: len(orders)}, nil
}

func (r *memoryRealm) ListAllOrders(
	_ context.Context, account domain.AccountID, source domain.Source,
) ([]domain.Order, error) {
	var out []domain.Order
	for _, order := range r.orders {
		if account != "" && order.Account != account {
			continue
		}
		if source != "" && order.Source != source {
			continue
		}
		out = append(out, order)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}

func (r *memoryRealm) CountOrders(context.Context) (int, error) {
	return len(r.orders), nil
}

func (r *memoryRealm) CountOrdersSince(_ context.Context, since time.Time) (int, error) {
	var n int
	for _, order := range r.orders {
		if !order.At.Before(since) {
			n++
		}
	}
	return n, nil
}

func (r *memoryRealm) RecordOrderSettlement(
	_ context.Context, st domain.OrderSettlement,
) error {
	if !st.Order.IsZero() {
		order, ok := r.orders[st.Order]
		if !ok {
			return domain.ErrNotFound
		}
		if len(st.AllowedFrom) > 0 && !slices.Contains(st.AllowedFrom, order.Status) {
			return domain.ErrConflict
		}
		order.Status = st.OrderStatus
		if st.SetLock {
			order.Lock = append([]byte(nil), st.Lock...)
		}
		r.orders[st.Order] = order
	}
	for _, balance := range st.Balances {
		key := balanceKey(st.Account, balance.Asset)
		current := r.balances[key]
		current.Account = st.Account
		current.Asset = balance.Asset
		if balance.Outcome.BalanceResult != "" {
			current.Available = balance.Outcome.BalanceResult
		}
		if balance.Outcome.HeldResult != "" {
			current.Held = balance.Outcome.HeldResult
		}
		if balance.Outcome.IncomingResult != "" {
			current.Incoming = balance.Outcome.IncomingResult
		}
		if balance.Outcome.RealizedPnlResult != "" {
			current.RealizedPnl = balance.Outcome.RealizedPnlResult
		}
		r.balances[key] = current
	}
	for _, block := range st.Blocks {
		account, ok := r.accounts[block.Account]
		if !ok {
			continue
		}
		account.Blocked = true
		account.BlockReason = block.Reason
		r.accounts[block.Account] = account
	}
	for _, event := range st.Events {
		if event.Order.IsZero() {
			event.Order = st.Order
		}
		if event.ExternalID.IsZero() {
			event.ExternalID = r.nextExternalID()
		}
		if event.At.IsZero() {
			event.At = time.Now().UTC()
		}
		r.events = append(r.events, event)
	}
	if st.Trade != nil {
		trade := *st.Trade
		if trade.ExternalID.IsZero() {
			trade.ExternalID = r.nextExternalID()
		}
		if trade.At.IsZero() {
			trade.At = time.Now().UTC()
		}
		r.trades = append(r.trades, trade)
	}
	return nil
}

func (r *memoryRealm) AppendOrderEvent(
	_ context.Context, event domain.OrderEvent,
) (domain.OrderEvent, error) {
	if event.ExternalID.IsZero() {
		event.ExternalID = r.nextExternalID()
	}
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	r.events = append(r.events, event)
	return event, nil
}

func (r *memoryRealm) ListOrderEvents(
	_ context.Context, order domain.ExternalID,
) ([]domain.OrderEvent, error) {
	var out []domain.OrderEvent
	for _, event := range r.events {
		if event.Order == order {
			out = append(out, event)
		}
	}
	return out, nil
}

func (r *memoryRealm) CreateTrade(
	_ context.Context, trade domain.Trade,
) (domain.Trade, error) {
	if trade.ExternalID.IsZero() {
		trade.ExternalID = r.nextExternalID()
	}
	if trade.At.IsZero() {
		trade.At = time.Now().UTC()
	}
	r.trades = append(r.trades, trade)
	return trade, nil
}

func (r *memoryRealm) ListTrades(
	ctx context.Context, account domain.AccountID, source domain.Source, n int,
) ([]domain.Trade, error) {
	if n <= 0 {
		return []domain.Trade{}, nil
	}
	all, err := r.ListAllTrades(ctx, account, source)
	if err != nil || len(all) <= n {
		return all, err
	}
	return all[:n], nil
}

func (r *memoryRealm) ListAllTrades(
	_ context.Context, account domain.AccountID, source domain.Source,
) ([]domain.Trade, error) {
	var out []domain.Trade
	for _, trade := range r.trades {
		if account != "" && trade.Account != account {
			continue
		}
		if source != "" && trade.Source != source {
			continue
		}
		out = append(out, trade)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}

func (r *memoryRealm) ListTradeRows(
	ctx context.Context, filter store.TradeListFilter,
) (store.TradeListPage, error) {
	trades, err := r.ListAllTrades(ctx, "", "")
	if err != nil {
		return store.TradeListPage{}, err
	}
	out := make([]domain.Trade, 0, len(trades))
	for _, trade := range trades {
		if !filter.ExternalID.IsZero() && trade.ExternalID != filter.ExternalID {
			continue
		}
		if !textMatches(filter.Account, trade.Account.String()) ||
			!textMatches(filter.BaseAsset, trade.BaseAsset) ||
			!textMatches(filter.QuoteAsset, trade.QuoteAsset) {
			continue
		}
		if filter.Side != nil && trade.Side != *filter.Side {
			continue
		}
		if filter.Source != "" && trade.Source != filter.Source {
			continue
		}
		out = append(out, trade)
	}
	total := len(out)
	if filter.Page.Limit > 0 {
		start := min(max(filter.Page.Offset, 0), len(out))
		end := min(start+filter.Page.Limit, len(out))
		out = out[start:end]
	}
	return store.TradeListPage{Rows: out, Total: total}, nil
}

func (r *memoryRealm) AppendAudit(_ context.Context, entry store.AuditEntry) error {
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

func textMatches(matcher store.TextMatcher, value string) bool {
	if matcher.Empty() {
		return true
	}
	value = strings.ToLower(value)
	if matcher.AnchorStart && matcher.AnchorEnd && len(matcher.Fragments) == 1 {
		return value == strings.ToLower(matcher.Fragments[0])
	}
	pos := 0
	for i, fragment := range matcher.Fragments {
		fragment = strings.ToLower(fragment)
		idx := strings.Index(value[pos:], fragment)
		if idx < 0 {
			return false
		}
		if i == 0 && matcher.AnchorStart && idx != 0 {
			return false
		}
		pos += idx + len(fragment)
	}
	if matcher.AnchorEnd && len(matcher.Fragments) > 0 {
		return strings.HasSuffix(
			value,
			strings.ToLower(matcher.Fragments[len(matcher.Fragments)-1]),
		)
	}
	return true
}

func textMatchesAny(matcher store.TextMatcher, values ...string) bool {
	if matcher.Empty() {
		return true
	}
	for _, value := range values {
		if textMatches(matcher, value) {
			return true
		}
	}
	return false
}

func adjustmentStatus(rec domain.AccountAdjustmentRecord) domain.AdjustmentStatus {
	if rec.Rejected != nil {
		return domain.AdjustmentStatusRejected
	}
	return domain.AdjustmentStatusAccepted
}

func (r *memoryRealm) CreateMarketDataInstance(
	_ context.Context, instance domain.MarketDataInstance,
) (domain.MarketDataInstance, error) {
	for _, existing := range r.instances {
		if existing.Label == instance.Label {
			return domain.MarketDataInstance{}, domain.ErrAlreadyExists
		}
	}
	if instance.ExternalID.IsZero() {
		instance.ExternalID = r.nextExternalID()
	}
	r.instances[instance.ExternalID] = instance
	return instance, nil
}

func (r *memoryRealm) GetMarketDataInstance(
	_ context.Context, id domain.ExternalID,
) (domain.MarketDataInstance, bool, error) {
	instance, ok := r.instances[id]
	return instance, ok, nil
}

func (r *memoryRealm) ListMarketDataInstances(
	context.Context,
) ([]domain.MarketDataInstance, error) {
	out := make([]domain.MarketDataInstance, 0, len(r.instances))
	for _, instance := range r.instances {
		out = append(out, instance)
	}
	return out, nil
}

func (r *memoryRealm) ListEnabledMarketDataInstances(
	context.Context,
) ([]domain.MarketDataInstance, error) {
	var out []domain.MarketDataInstance
	for _, instance := range r.instances {
		if instance.Enabled {
			out = append(out, instance)
		}
	}
	return out, nil
}

func (r *memoryRealm) SetMarketDataInstanceEnabled(
	_ context.Context, id domain.ExternalID, enabled bool,
) error {
	instance, ok := r.instances[id]
	if !ok {
		return domain.ErrNotFound
	}
	instance.Enabled = enabled
	r.instances[id] = instance
	return nil
}

func (r *memoryRealm) UpdateMarketDataInstanceSettings(
	_ context.Context, id domain.ExternalID, label, credentials string,
) error {
	instance, ok := r.instances[id]
	if !ok {
		return domain.ErrNotFound
	}
	instance.Label = label
	instance.Credentials = credentials
	r.instances[id] = instance
	return nil
}

func (r *memoryRealm) DeleteMarketDataInstance(
	_ context.Context, id domain.ExternalID, _ bool,
) error {
	if _, ok := r.instances[id]; !ok {
		return domain.ErrNotFound
	}
	delete(r.instances, id)
	return nil
}

func (r *memoryRealm) UpsertMarketDataInstrument(
	_ context.Context, instrument domain.MarketDataInstrument,
) error {
	r.instruments[instrumentKey(instrument.Instance, instrument.ExternalSymbol)] = instrument
	return nil
}

func (r *memoryRealm) ListMarketDataInstruments(
	_ context.Context, instance domain.ExternalID,
) ([]domain.MarketDataInstrument, error) {
	var out []domain.MarketDataInstrument
	for _, instrument := range r.instruments {
		if instrument.Instance == instance {
			out = append(out, instrument)
		}
	}
	return out, nil
}

func (r *memoryRealm) ListEnabledMarketDataInstruments(
	_ context.Context, instance domain.ExternalID,
) ([]domain.MarketDataInstrument, error) {
	var out []domain.MarketDataInstrument
	for _, instrument := range r.instruments {
		if instrument.Instance == instance && instrument.Enabled {
			out = append(out, instrument)
		}
	}
	return out, nil
}

func (r *memoryRealm) SetMarketDataInstrumentEnabled(
	_ context.Context, instance domain.ExternalID, externalSymbol string, enabled bool,
) error {
	key := instrumentKey(instance, externalSymbol)
	instrument, ok := r.instruments[key]
	if !ok {
		return domain.ErrNotFound
	}
	instrument.Enabled = enabled
	r.instruments[key] = instrument
	return nil
}

func (r *memoryRealm) DeleteMarketDataInstrument(
	_ context.Context, instance domain.ExternalID, externalSymbol string,
) error {
	key := instrumentKey(instance, externalSymbol)
	if _, ok := r.instruments[key]; !ok {
		return domain.ErrNotFound
	}
	delete(r.instruments, key)
	return nil
}

func (r *memoryRealm) UpsertMarketDataQuote(
	_ context.Context, quote domain.MarketDataQuote,
) error {
	r.quotes[instrumentKey(quote.Instance, quote.ExternalSymbol)] = quote
	return nil
}

func (r *memoryRealm) ListMarketDataQuotes(
	_ context.Context, instance domain.ExternalID,
) ([]domain.MarketDataQuote, error) {
	var out []domain.MarketDataQuote
	for _, quote := range r.quotes {
		if instance.IsZero() || quote.Instance == instance {
			out = append(out, quote)
		}
	}
	return out, nil
}

func (r *memoryRealm) UpsertSigningKey(_ context.Context, key domain.SigningKey) error {
	r.signingKeys[key.KeyID] = key
	return nil
}

func (r *memoryRealm) GetActiveSigningKey(
	context.Context,
) (domain.SigningKey, bool, error) {
	for _, key := range r.signingKeys {
		if key.Active {
			return key, true, nil
		}
	}
	return domain.SigningKey{}, false, nil
}

func (r *memoryRealm) GetSigningKey(
	_ context.Context, keyID string,
) (domain.SigningKey, error) {
	key, ok := r.signingKeys[keyID]
	if !ok {
		return domain.SigningKey{}, domain.ErrNotFound
	}
	return key, nil
}

func (r *memoryRealm) ListSigningKeys(context.Context) ([]domain.SigningKey, error) {
	out := make([]domain.SigningKey, 0, len(r.signingKeys))
	for _, key := range r.signingKeys {
		key.PrivateKey = nil
		out = append(out, key)
	}
	return out, nil
}

func (r *memoryRealm) DeactivateAllSigningKeys(context.Context) error {
	for id, key := range r.signingKeys {
		key.Active = false
		r.signingKeys[id] = key
	}
	return nil
}

func (r *memoryRealm) GetSigningConfig(
	_ context.Context, key string,
) (string, bool, error) {
	value, ok := r.signingConfig[key]
	return value, ok, nil
}

func (r *memoryRealm) SetSigningConfig(_ context.Context, key, value string) error {
	r.signingConfig[key] = value
	return nil
}

func (r *memoryRealm) ListMcpAccess(context.Context) (map[string]bool, error) {
	out := make(map[string]bool, len(r.mcpAccess))
	for key, value := range r.mcpAccess {
		out[key] = value
	}
	return out, nil
}

func (r *memoryRealm) SetMcpAccess(
	_ context.Context, command string, enabled bool,
) error {
	r.mcpAccess[command] = enabled
	return nil
}

func (r *memoryRealm) GetUserSetting(
	_ context.Context, userID, key string,
) (string, bool, error) {
	setting, ok := r.userSettings[settingKey(userID, key)]
	return setting.Value, ok, nil
}

func (r *memoryRealm) SetUserSetting(
	_ context.Context, userID, key, value string,
) error {
	r.userSettings[settingKey(userID, key)] = domain.UserSetting{
		UserID: userID,
		Key:    key,
		Value:  value,
	}
	return nil
}

func (r *memoryRealm) ListUserSettings(context.Context) ([]domain.UserSetting, error) {
	out := make([]domain.UserSetting, 0, len(r.userSettings))
	for _, setting := range r.userSettings {
		out = append(out, setting)
	}
	return out, nil
}

func (r *memoryRealm) UpsertReservationIntent(
	_ context.Context, intent domain.ReservationIntent,
) error {
	r.reservations[intent.ApprovalID] = intent
	return nil
}

func (r *memoryRealm) GetReservationIntent(
	_ context.Context, approvalID string,
) (domain.ReservationIntent, bool, error) {
	intent, ok := r.reservations[approvalID]
	return intent, ok, nil
}

func (r *memoryRealm) ListOpenReservationIntents(
	context.Context,
) ([]domain.ReservationIntent, error) {
	var out []domain.ReservationIntent
	for _, intent := range r.reservations {
		if intent.State == domain.ReservationIntentStateHeld {
			out = append(out, intent)
		}
	}
	return out, nil
}

func (r *memoryRealm) SetReservationIntentState(
	_ context.Context, approvalID string, state domain.ReservationIntentState,
) error {
	intent, ok := r.reservations[approvalID]
	if !ok {
		return domain.ErrNotFound
	}
	intent.State = state
	r.reservations[approvalID] = intent
	return nil
}

func (r *memoryRealm) ResolveOrderReservation(
	_ context.Context, resolution domain.ReservationResolution,
) error {
	intent, ok := r.reservations[resolution.ApprovalID]
	if ok {
		intent.State = resolution.IntentState
		r.reservations[resolution.ApprovalID] = intent
	}
	if resolution.Order.IsZero() {
		return nil
	}
	order, ok := r.orders[resolution.Order]
	if !ok {
		return domain.ErrNotFound
	}
	if len(resolution.AllowedFrom) > 0 &&
		!slices.Contains(resolution.AllowedFrom, order.Status) {
		return domain.ErrConflict
	}
	order.Status = resolution.OrderStatus
	r.orders[resolution.Order] = order
	for _, event := range resolution.Events {
		if event.ExternalID.IsZero() {
			event.ExternalID = r.nextExternalID()
		}
		if event.At.IsZero() {
			event.At = time.Now().UTC()
		}
		r.events = append(r.events, event)
	}
	return nil
}

func (r *memoryRealm) ApplyBusinessCSVImport(
	ctx context.Context, in store.BusinessCSVImport,
) error {
	snapshot := r.exportData(ctx)
	for _, group := range in.Groups {
		if group.Exists {
			current, ok := r.groups[group.Group.Code]
			if !ok {
				r.restoreData(snapshot)
				return domain.ErrInvalid
			}
			group.Group.EngineGroupID = current.EngineGroupID
			r.groups[group.Group.Code] = group.Group
		} else {
			if _, err := r.CreateGroup(ctx, group.Group); err != nil {
				r.restoreData(snapshot)
				return err
			}
		}
	}
	for _, account := range in.Accounts {
		if account.Exists {
			current, ok := r.accounts[account.Account.Code]
			if !ok {
				r.restoreData(snapshot)
				return domain.ErrInvalid
			}
			account.Account.EngineAccountID = current.EngineAccountID
			r.accounts[account.Account.Code] = account.Account
		} else {
			if _, err := r.CreateAccount(ctx, account.Account); err != nil {
				r.restoreData(snapshot)
				return err
			}
		}
	}
	for _, balance := range in.Balances {
		if err := r.UpsertBalance(ctx, balance); err != nil {
			r.restoreData(snapshot)
			return err
		}
	}
	for _, adjustment := range in.Adjustments {
		if _, err := r.AppendAdjustment(ctx, adjustment); err != nil {
			r.restoreData(snapshot)
			return err
		}
	}
	for _, audit := range in.Audits {
		if err := r.AppendAudit(ctx, audit); err != nil {
			r.restoreData(snapshot)
			return err
		}
	}
	return nil
}

func (r *memoryRealm) ExportBackup(
	ctx context.Context, scope backup.Scope,
) (backup.Archive, error) {
	return backup.NewArchive(
		time.Now().UTC(),
		"memory",
		backup.RealmLabel{Code: string(domain.DefaultRealm)},
		scope,
		r.exportData(ctx),
	), nil
}

func (r *memoryRealm) RestoreBackup(
	ctx context.Context, archive backup.Archive, opts backup.RestoreOptions,
) (backup.RestoreSummary, error) {
	if opts.Mode == "" {
		return backup.RestoreSummary{}, domain.ErrInvalid
	}
	data := archive.Data
	if opts.Scope.All || backup.TouchesRuntime(opts.Scope) {
		// Runtime sections are replaced wholesale in the test store. This is
		// enough for LocalNode rollback/rebuild tests and keeps the fake honest
		// about the restart signal.
		r.restoreData(data)
	} else {
		for key := range r.mcpAccess {
			delete(r.mcpAccess, key)
		}
		for key, value := range data.McpAccess {
			r.mcpAccess[key] = value
		}
		for _, setting := range data.UserSettings {
			r.userSettings[settingKey(setting.UserID, setting.Key)] = setting
		}
	}
	summary := backup.NewSummary()
	summary.RestartRequired = backup.TouchesRuntime(opts.Scope)
	for _, section := range opts.Scope.IncludedSections() {
		summary.AddApplied(section, r.sectionCount(ctx, section))
	}
	return summary, nil
}

func (r *memoryRealm) exportData(context.Context) backup.Data {
	data := backup.Data{
		Assets:                make([]domain.Asset, 0, len(r.assets)),
		Principals:            make([]domain.Principal, 0, len(r.principals)),
		Groups:                make([]backup.AccountGroup, 0, len(r.groups)),
		Accounts:              make([]backup.Account, 0, len(r.accounts)),
		Balances:              make([]domain.Balance, 0, len(r.balances)),
		RateLimits:            make([]domain.LimitRate, 0, len(r.rateLimits)),
		OrderSizeLimits:       make([]domain.LimitOrderSize, 0, len(r.orderSizeLimits)),
		PnlBoundsLimits:       make([]domain.LimitPnlBounds, 0, len(r.pnlBoundsLimits)),
		Adjustments:           append([]domain.AccountAdjustmentRecord(nil), r.adjustments...),
		Orders:                make([]backup.OrderRecord, 0, len(r.orders)),
		OrderEvents:           append([]domain.OrderEvent(nil), r.events...),
		Trades:                append([]domain.Trade(nil), r.trades...),
		Audit:                 append([]domain.AuditRow(nil), r.audit...),
		MarketDataInstances:   make([]domain.MarketDataInstance, 0, len(r.instances)),
		MarketDataInstruments: make([]domain.MarketDataInstrument, 0, len(r.instruments)),
		MarketDataQuotes:      make([]domain.MarketDataQuote, 0, len(r.quotes)),
		SigningKeys:           make([]backup.SigningKey, 0, len(r.signingKeys)),
		SigningConfig:         make([]backup.SigningConfigEntry, 0, len(r.signingConfig)),
		McpAccess:             make(map[string]bool, len(r.mcpAccess)),
		UserSettings:          make([]domain.UserSetting, 0, len(r.userSettings)),
	}
	for _, asset := range r.assets {
		data.Assets = append(data.Assets, asset)
	}
	for _, principal := range r.principals {
		data.Principals = append(data.Principals, principal)
	}
	for _, group := range r.groups {
		data.Groups = append(data.Groups, backup.AccountGroup{
			Code: group.Code, Title: group.Title, Notes: group.Notes,
			BlockReason: group.BlockReason, Blocked: group.Blocked,
		})
	}
	for _, account := range r.accounts {
		data.Accounts = append(data.Accounts, backup.Account{
			Code: string(account.Code), Title: account.Title,
			GroupCode: account.GroupCode, Notes: account.Notes,
			BlockReason: account.BlockReason, Blocked: account.Blocked,
		})
	}
	for _, balance := range r.balances {
		data.Balances = append(data.Balances, balance)
	}
	for _, limit := range r.rateLimits {
		data.RateLimits = append(data.RateLimits, limit)
	}
	for _, limit := range r.orderSizeLimits {
		data.OrderSizeLimits = append(data.OrderSizeLimits, limit)
	}
	for _, limit := range r.pnlBoundsLimits {
		data.PnlBoundsLimits = append(data.PnlBoundsLimits, limit)
	}
	for id, order := range r.orders {
		record := backup.OrderRecord{Order: order}
		if approval, ok := r.approvals[id]; ok {
			record.Approval = &approval
		}
		data.Orders = append(data.Orders, record)
	}
	for _, instance := range r.instances {
		data.MarketDataInstances = append(data.MarketDataInstances, instance)
	}
	for _, instrument := range r.instruments {
		data.MarketDataInstruments = append(data.MarketDataInstruments, instrument)
	}
	for _, quote := range r.quotes {
		data.MarketDataQuotes = append(data.MarketDataQuotes, quote)
	}
	for _, key := range r.signingKeys {
		data.SigningKeys = append(data.SigningKeys, backup.SigningKey{
			CreatedAt: key.CreatedAt, KeyID: key.KeyID, Alg: key.Alg,
			PublicKey: key.PublicKey, PrivateKey: key.PrivateKey,
			Active: key.Active,
		})
	}
	for key, value := range r.signingConfig {
		data.SigningConfig = append(data.SigningConfig,
			backup.SigningConfigEntry{Key: key, Value: value})
	}
	for key, value := range r.mcpAccess {
		data.McpAccess[key] = value
	}
	for _, setting := range r.userSettings {
		data.UserSettings = append(data.UserSettings, setting)
	}
	return data
}

func (r *memoryRealm) restoreData(data backup.Data) {
	r.assets = map[string]domain.Asset{}
	for _, asset := range data.Assets {
		r.assets[asset.Code] = asset
	}
	r.principals = map[string]domain.Principal{}
	for _, principal := range data.Principals {
		r.principals[principal.Code] = principal
	}
	r.groups = map[string]domain.AccountGroup{}
	for _, group := range data.Groups {
		r.nextGroupID++
		r.groups[group.Code] = domain.AccountGroup{
			Code: group.Code, Title: group.Title, Notes: group.Notes,
			BlockReason: group.BlockReason, Blocked: group.Blocked,
			EngineGroupID: domain.EngineGroupID(r.nextGroupID),
		}
	}
	r.accounts = map[domain.AccountID]domain.Account{}
	for _, account := range data.Accounts {
		r.nextAccountID++
		code := domain.AccountID(account.Code)
		r.accounts[code] = domain.Account{
			Code: code, Title: account.Title, GroupCode: account.GroupCode,
			Notes: account.Notes, BlockReason: account.BlockReason,
			Blocked: account.Blocked, EngineAccountID: r.nextAccountID,
		}
	}
	r.balances = map[string]domain.Balance{}
	for _, balance := range data.Balances {
		r.balances[balanceKey(balance.Account, balance.Asset)] = balance
	}
	r.rateLimits = map[string]domain.LimitRate{}
	for _, limit := range data.RateLimits {
		r.rateLimits[limitKey(limit.Scope, limit.Account, limit.Asset)] = limit
	}
	r.orderSizeLimits = map[string]domain.LimitOrderSize{}
	for _, limit := range data.OrderSizeLimits {
		r.orderSizeLimits[limitKey(limit.Scope, limit.Account, limit.Asset)] = limit
	}
	r.pnlBoundsLimits = map[string]domain.LimitPnlBounds{}
	for _, limit := range data.PnlBoundsLimits {
		r.pnlBoundsLimits[limitKey(limit.Scope, limit.Account, limit.Asset)] = limit
	}
	r.adjustments = append([]domain.AccountAdjustmentRecord(nil), data.Adjustments...)
	r.orders = map[domain.ExternalID]domain.Order{}
	r.approvals = map[domain.ExternalID]domain.OrderApproval{}
	for _, record := range data.Orders {
		r.orders[record.Order.ExternalID] = record.Order
		if record.Approval != nil {
			r.approvals[record.Order.ExternalID] = *record.Approval
		}
	}
	r.events = append([]domain.OrderEvent(nil), data.OrderEvents...)
	r.trades = append([]domain.Trade(nil), data.Trades...)
	r.audit = append([]domain.AuditRow(nil), data.Audit...)
	r.instances = map[domain.ExternalID]domain.MarketDataInstance{}
	for _, instance := range data.MarketDataInstances {
		r.instances[instance.ExternalID] = instance
	}
	r.instruments = map[string]domain.MarketDataInstrument{}
	for _, instrument := range data.MarketDataInstruments {
		r.instruments[instrumentKey(instrument.Instance, instrument.ExternalSymbol)] = instrument
	}
	r.quotes = map[string]domain.MarketDataQuote{}
	for _, quote := range data.MarketDataQuotes {
		r.quotes[instrumentKey(quote.Instance, quote.ExternalSymbol)] = quote
	}
	r.signingKeys = map[string]domain.SigningKey{}
	for _, key := range data.SigningKeys {
		r.signingKeys[key.KeyID] = domain.SigningKey{
			CreatedAt: key.CreatedAt, KeyID: key.KeyID, Alg: key.Alg,
			PublicKey: key.PublicKey, PrivateKey: key.PrivateKey,
			Active: key.Active,
		}
	}
	r.signingConfig = map[string]string{}
	for _, entry := range data.SigningConfig {
		r.signingConfig[entry.Key] = entry.Value
	}
	r.mcpAccess = map[string]bool{}
	for key, value := range data.McpAccess {
		r.mcpAccess[key] = value
	}
	r.userSettings = map[string]domain.UserSetting{}
	for _, setting := range data.UserSettings {
		r.userSettings[settingKey(setting.UserID, setting.Key)] = setting
	}
}

func (r *memoryRealm) sectionCount(_ context.Context, section backup.Section) int {
	switch section {
	case backup.SectionAccountsGroups:
		return len(r.groups) + len(r.accounts)
	case backup.SectionPositions:
		return len(r.balances)
	case backup.SectionRiskLimits:
		return len(r.rateLimits) + len(r.orderSizeLimits) + len(r.pnlBoundsLimits)
	case backup.SectionMarketData:
		return len(r.instances) + len(r.instruments)
	case backup.SectionMarketDataQuotes:
		return len(r.quotes)
	case backup.SectionGeneralSettings:
		return len(r.mcpAccess) + len(r.signingConfig)
	case backup.SectionUserSettings:
		return len(r.userSettings)
	case backup.SectionActivityHistory:
		return len(r.adjustments) + len(r.orders) + len(r.events) + len(r.trades)
	case backup.SectionAuditLog:
		return len(r.audit)
	default:
		return 0
	}
}
