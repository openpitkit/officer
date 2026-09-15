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

package httpapi

import (
	"context"
	"fmt"
	"time"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/businesscsv"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
)

type fakeService struct {
	accounts              []domain.Account
	accountRows           []store.AccountListRow
	accountFilter         store.AccountListFilter
	balanceFilter         store.BalanceListFilter
	orderFilter           store.OrderListFilter
	limits                node.AccountLimits
	policyRows            []store.PolicyListRow
	policyFilter          store.PolicyListFilter
	policyErr             error
	auditRows             []domain.AuditRow
	auditListPage         *store.AuditListPage
	auditFilter           domain.AuditFilter
	auditListFilter       store.AuditListFilter
	assets                []domain.Asset
	assetFilter           store.AssetListFilter
	assetClasses          []domain.AssetClass
	assetClassRows        []store.AssetClassListRow
	assetClassFilter      store.AssetClassListFilter
	groups                []domain.AccountGroup
	groupRows             []store.GroupListRow
	groupFilter           store.GroupListFilter
	balances              []domain.Balance
	balanceRealizedPnl    domain.Balance
	realizedPnlAsset      string
	realizedPnlValue      string
	adjustments           []domain.AccountAdjustmentRecord
	adjustmentPage        *store.AdjustmentListPage
	adjustmentFilter      store.AdjustmentListFilter
	orders                []domain.Order
	orderPage             *backend.OrderListPage
	trades                []domain.Trade
	tradePage             *store.TradeListPage
	tradeFilter           store.TradeListFilter
	orderDetail           domain.OrderDetail
	adjustment            domain.AccountAdjustmentRecord
	submitOrder           domain.Order
	execReportIn          domain.ExecutionReportInput
	checkResult           domain.CheckResult
	overview              backend.Overview
	serviceInfo           backend.ServiceInfo
	backupArchive         backup.Archive
	backupFilename        string
	backupSummary         backup.RestoreSummary
	backupErr             error
	csvExport             businesscsv.ExportFile
	csvExportReq          backend.BusinessCSVExportRequest
	csvErr                error
	restoreArchive        backup.Archive
	restoreOptions        backup.RestoreOptions
	resetCalled           bool
	resetErr              error
	marketData            backend.MarketDataStatus
	mdVerify              backend.MarketDataSymbolVerification
	mdVerifyErr           error
	mdSearch              backend.MarketDataSymbolSearch
	mdSearchInput         backend.MarketDataSymbolSearchInput
	mdSearchErr           error
	mdCreateResult        domain.MarketDataInstance
	status                backend.Status
	statusErr             error
	welcomeSeen           bool
	createAssetErr        error
	updateAssetErr        error
	deleteAssetErr        error
	deleteAssetForce      bool
	createAssetClassErr   error
	updateAssetClassErr   error
	deleteAssetClassErr   error
	deleteAssetClassForce bool
	deleteGroupForce      bool
	assetClassErr         error
	createErr             error
	blockErr              error
	unblockErr            error
	stateErr              error
	execReportErr         error
	execReportResult      engine.ExecutionReportResult
	listLimErr            error
	putLimErr             error
	delLimErr             error
	auditErr              error
	groupErr              error

	// Captured account writes, so a test can prove a rejected request changed
	// nothing. The account fixtures themselves stay untouched.
	createdAccounts    []domain.Account
	accountNotesWrites []accountNotesWrite

	// Captured typed-limit puts and delete target, for round-trip assertions.
	rateLimitPut               domain.LimitRate
	orderSizeLimitPut          domain.LimitOrderSize
	spotFundsPnlBoundsLimitPut domain.LimitSpotFundsPnlBounds
	deleteLimitTarget          node.LimitTarget
	// Captured market-data mutation inputs.
	mdCreateInstance   domain.MarketDataInstance
	mdUpsertInstrument domain.MarketDataInstrument

	// Error fields for list handlers whose service methods otherwise return a
	// hardcoded nil; default nil so existing tests are unaffected.
	listAccountsErr error
	balancesErr     error
	ordersErr       error
	tradesErr       error
	allAdjErr       error

	mcpCommands  []backend.McpCommand
	mcpAccessErr error
	setMcpErr    error
	setMcpCalls  []setMcpCall
	mdCalls      []string

	// Signing / approval fields.
	signingKey            domain.SigningKey
	signingKeys           []domain.SigningKey
	activePublicKey       string
	activePublicKeyFormat string
	noESign               bool
	noESignSet            bool
	noESignCalls          int
	approvalToken         backend.ApprovalToken
	attestation           backend.Attestation
	submitTokenMode       string
	signingErr            error
	// confirmErr/cancelErr inject a resolution failure (e.g. a terminal-order
	// conflict) into ConfirmExecution/CancelOrder, kept distinct from signingErr so
	// a test can drive the terminal-order path without touching the signing setup.
	confirmErr           error
	cancelErr            error
	cancelCalls          int
	cancelLeavesQuantity string
	// Public-key-by-id resolution. publicKeysByID maps keyId to its exported
	// public material; a missing id reports domain.ErrNotFound so the rotation and
	// 404 paths can be exercised. keyByIDFormat/keyByIDLast capture the last call.
	publicKeysByID map[string]string
	keyByIDFormat  string
	keyByIDLast    string

	// Captured caller-supplied external ids for the user-create surfaces. The
	// adjustment id is captured on ApplyAdjustment; the order id is captured on
	// SubmitOrderToken (which CREATES the order once, mirroring the backend).
	adjustmentExternalID domain.ExternalID
	submitOrderIn        domain.Order

	// missingAccount is the missing-account choice the last mutating command
	// received, so a handler test can assert the query parameter was threaded.
	missingAccount domain.MissingAccountPolicy

	// orders submitted-then-resolved, keyed by their used external id, so a fake
	// SubmitOrderToken can create once and confirm/cancel resolve the same order.
	submittedOrders map[string]domain.Order
}

type setMcpCall struct {
	command string
	enabled bool
}

func (f *fakeService) Status(_ context.Context) (backend.Status, error) {
	return f.status, f.statusErr
}
func (f *fakeService) ListAccounts(_ context.Context) ([]domain.Account, error) {
	return f.accounts, f.listAccountsErr
}
func (f *fakeService) ListAccountRows(
	_ context.Context, filter store.AccountListFilter,
) (store.AccountListPage, error) {
	f.accountFilter = filter
	if f.listAccountsErr != nil {
		return store.AccountListPage{}, f.listAccountsErr
	}
	if f.accountRows != nil {
		return store.AccountListPage{
			Rows:  f.accountRows,
			Total: len(f.accountRows),
		}, nil
	}
	out := make([]store.AccountListRow, 0, len(f.accounts))
	for _, account := range f.accounts {
		out = append(out, store.AccountListRow{Account: account})
	}
	return store.AccountListPage{Rows: out, Total: len(out)}, nil
}
func (f *fakeService) ExportBackup(
	_ context.Context, _ backup.Scope,
) (backup.Archive, string, error) {
	return f.backupArchive, f.backupFilename, f.backupErr
}
func (f *fakeService) RestoreBackup(
	_ context.Context,
	archive backup.Archive,
	opts backup.RestoreOptions,
) (backup.RestoreSummary, error) {
	f.restoreArchive = archive
	f.restoreOptions = opts
	return f.backupSummary, f.backupErr
}
func (f *fakeService) ExportBusinessCSV(
	_ context.Context,
	req backend.BusinessCSVExportRequest,
) (businesscsv.ExportFile, error) {
	f.csvExportReq = req
	return f.csvExport, f.csvErr
}
func (f *fakeService) ResetDatabase(_ context.Context) error {
	f.resetCalled = true
	return f.resetErr
}
func (f *fakeService) ListAssets(_ context.Context) ([]domain.Asset, error) {
	return f.assets, nil
}
func (f *fakeService) ListAssetRows(
	_ context.Context, filter store.AssetListFilter,
) (store.AssetListPage, error) {
	f.assetFilter = filter
	return store.AssetListPage{Rows: f.assets, Total: len(f.assets)}, nil
}
func (f *fakeService) CreateAsset(_ context.Context, asset domain.Asset) (domain.Asset, error) {
	if f.createAssetErr != nil {
		return domain.Asset{}, f.createAssetErr
	}
	f.assets = append(f.assets, asset)
	return asset, nil
}
func (f *fakeService) UpdateAsset(
	_ context.Context, oldCode string, asset domain.Asset,
) (domain.Asset, error) {
	if f.updateAssetErr != nil {
		return domain.Asset{}, f.updateAssetErr
	}
	for i, a := range f.assets {
		if a.Code == oldCode {
			f.assets[i] = asset
			return asset, nil
		}
	}
	return domain.Asset{}, domain.ErrNotFound
}
func (f *fakeService) ListAssetClasses(_ context.Context) ([]domain.AssetClass, error) {
	return f.assetClasses, f.assetClassErr
}
func (f *fakeService) ListAssetClassRows(
	_ context.Context, filter store.AssetClassListFilter,
) (store.AssetClassListPage, error) {
	f.assetClassFilter = filter
	if f.assetClassErr != nil {
		return store.AssetClassListPage{}, f.assetClassErr
	}
	if f.assetClassRows != nil {
		return store.AssetClassListPage{Rows: f.assetClassRows, Total: len(f.assetClassRows)}, nil
	}
	out := make([]store.AssetClassListRow, 0, len(f.assetClasses))
	for _, class := range f.assetClasses {
		out = append(out, store.AssetClassListRow{Class: class})
	}
	return store.AssetClassListPage{Rows: out, Total: len(out)}, nil
}
func (f *fakeService) CreateAssetClass(
	_ context.Context, class domain.AssetClass,
) (domain.AssetClass, error) {
	if f.createAssetClassErr != nil {
		return domain.AssetClass{}, f.createAssetClassErr
	}
	f.assetClasses = append(f.assetClasses, class)
	return class, nil
}
func (f *fakeService) UpdateAssetClass(
	_ context.Context, oldCode string, class domain.AssetClass,
) (domain.AssetClass, error) {
	if f.updateAssetClassErr != nil {
		return domain.AssetClass{}, f.updateAssetClassErr
	}
	for i, c := range f.assetClasses {
		if c.Code == oldCode {
			f.assetClasses[i] = class
			return class, nil
		}
	}
	return domain.AssetClass{}, domain.ErrNotFound
}
func (f *fakeService) DeleteAssetClass(_ context.Context, code string, force bool) error {
	if f.deleteAssetClassErr != nil {
		return f.deleteAssetClassErr
	}
	f.deleteAssetClassForce = force
	for i, c := range f.assetClasses {
		if c.Code == code {
			f.assetClasses = append(f.assetClasses[:i], f.assetClasses[i+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}
func (f *fakeService) DeleteAsset(_ context.Context, code string, force bool) error {
	if f.deleteAssetErr != nil {
		return f.deleteAssetErr
	}
	f.deleteAssetForce = force
	for i, a := range f.assets {
		if a.Code == code {
			f.assets = append(f.assets[:i], f.assets[i+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}

// accountNotesWrite is one accepted SetAccountNotes call.
type accountNotesWrite struct {
	Code  domain.AccountID
	Notes string
}

func (f *fakeService) CreateAccount(_ context.Context, account domain.Account) (domain.Account, error) {
	if f.createErr != nil {
		return domain.Account{}, f.createErr
	}
	f.createdAccounts = append(f.createdAccounts, account)
	return account, nil
}
func (f *fakeService) UpdateAccount(
	_ context.Context, _ domain.AccountID, account domain.Account,
) (domain.Account, error) {
	if f.createErr != nil {
		return domain.Account{}, f.createErr
	}
	return account, nil
}
func (f *fakeService) GetAccountState(_ context.Context, id domain.AccountID) (domain.Account, node.AccountLimits, error) {
	if f.stateErr != nil {
		return domain.Account{}, node.AccountLimits{}, f.stateErr
	}
	for _, a := range f.accounts {
		if a.Code == id {
			return a, f.limits, nil
		}
	}
	return domain.Account{}, node.AccountLimits{}, domain.ErrNotFound
}
func (f *fakeService) BlockAccount(
	_ context.Context, _ domain.AccountID, _ string,
	missing domain.MissingAccountPolicy,
) error {
	f.missingAccount = missing
	return f.blockErr
}
func (f *fakeService) UnblockAccount(
	_ context.Context, _ domain.AccountID, missing domain.MissingAccountPolicy,
) error {
	f.missingAccount = missing
	return f.unblockErr
}
func (f *fakeService) DeleteAccount(
	_ context.Context, _ domain.AccountID, _ bool,
) error {
	return f.stateErr
}
func (f *fakeService) ListLimits(_ context.Context, _ domain.AccountID) (node.AccountLimits, error) {
	return f.limits, f.listLimErr
}
func (f *fakeService) ListPolicyRows(
	_ context.Context, filter store.PolicyListFilter,
) (store.PolicyListPage, error) {
	f.policyFilter = filter
	if f.policyErr != nil {
		return store.PolicyListPage{}, f.policyErr
	}
	return store.PolicyListPage{Rows: f.policyRows, Total: len(f.policyRows)}, nil
}
func (f *fakeService) PutRateLimit(
	_ context.Context, l domain.LimitRate, missing domain.MissingAccountPolicy,
) error {
	f.rateLimitPut = l
	f.missingAccount = missing
	return f.putLimErr
}
func (f *fakeService) PutOrderSizeLimit(
	_ context.Context, l domain.LimitOrderSize, missing domain.MissingAccountPolicy,
) error {
	f.orderSizeLimitPut = l
	f.missingAccount = missing
	return f.putLimErr
}
func (f *fakeService) PutSpotFundsPnlBoundsLimit(
	_ context.Context,
	l domain.LimitSpotFundsPnlBounds,
	missing domain.MissingAccountPolicy,
) error {
	f.spotFundsPnlBoundsLimitPut = l
	f.missingAccount = missing
	return f.putLimErr
}
func (f *fakeService) DeleteLimit(_ context.Context, t node.LimitTarget) error {
	f.deleteLimitTarget = t
	return f.delLimErr
}
func (f *fakeService) ListAudit(_ context.Context, _ int) ([]domain.AuditRow, error) {
	return f.auditRows, f.auditErr
}
func (f *fakeService) ListAuditFiltered(
	_ context.Context, filter domain.AuditFilter, _ int,
) ([]domain.AuditRow, error) {
	f.auditFilter = filter
	return f.auditRows, f.auditErr
}
func (f *fakeService) ListAuditRows(
	_ context.Context, filter store.AuditListFilter,
) (store.AuditListPage, error) {
	f.auditListFilter = filter
	if f.auditErr != nil {
		return store.AuditListPage{}, f.auditErr
	}
	if f.auditListPage != nil {
		return *f.auditListPage, nil
	}
	return store.AuditListPage{Rows: f.auditRows, Total: len(f.auditRows)}, nil
}
func (f *fakeService) ListMcpAccess(_ context.Context) ([]backend.McpCommand, error) {
	return f.mcpCommands, f.mcpAccessErr
}
func (f *fakeService) SetMcpAccess(_ context.Context, command string, enabled bool) error {
	if f.setMcpErr != nil {
		return f.setMcpErr
	}
	f.setMcpCalls = append(f.setMcpCalls, setMcpCall{command: command, enabled: enabled})
	return nil
}
func (f *fakeService) WelcomeSeen(_ context.Context) (bool, error) {
	return f.welcomeSeen, nil
}
func (f *fakeService) SetWelcomeSeen(_ context.Context, seen bool) error {
	f.welcomeSeen = seen
	return nil
}
func (f *fakeService) ListMarketData(_ context.Context) (backend.MarketDataStatus, error) {
	return f.marketData, f.stateErr
}
func (f *fakeService) CreateMarketDataInstance(
	_ context.Context, instance domain.MarketDataInstance,
) (domain.MarketDataInstance, error) {
	f.mdCreateInstance = instance
	f.mdCalls = append(f.mdCalls,
		fmt.Sprintf("create:%s:%s:%s:%v",
			instance.Provider, instance.Label, instance.Credentials, instance.Enabled))
	if !f.mdCreateResult.ExternalID.IsZero() {
		return f.mdCreateResult, f.stateErr
	}
	return instance, f.stateErr
}
func (f *fakeService) SetMarketDataInstanceEnabled(
	_ context.Context, id string, enabled bool,
) error {
	f.mdCalls = append(f.mdCalls, fmt.Sprintf("instance:%s:%v", id, enabled))
	return f.stateErr
}
func (f *fakeService) UpdateMarketDataInstanceSettings(
	_ context.Context, id, label, credentials string,
) error {
	f.mdCalls = append(f.mdCalls,
		fmt.Sprintf("settings:%s:%s:%s", id, label, credentials))
	return f.stateErr
}
func (f *fakeService) DeleteMarketDataInstance(
	_ context.Context, id string,
) error {
	f.mdCalls = append(f.mdCalls, fmt.Sprintf("delete-instance:%s", id))
	return f.stateErr
}
func (f *fakeService) UpsertMarketDataInstrument(
	_ context.Context, instrument domain.MarketDataInstrument,
) error {
	f.mdUpsertInstrument = instrument
	f.mdCalls = append(f.mdCalls, "upsert-instrument:"+instrument.ExternalSymbol)
	return f.stateErr
}
func (f *fakeService) SetMarketDataInstrumentEnabled(
	_ context.Context, instanceID, externalSymbol string, enabled bool,
) error {
	f.mdCalls = append(f.mdCalls,
		fmt.Sprintf("instrument:%s/%s:%v", instanceID, externalSymbol, enabled))
	return f.stateErr
}
func (f *fakeService) DeleteMarketDataInstrument(
	_ context.Context, instanceID, externalSymbol string,
) error {
	f.mdCalls = append(f.mdCalls, "delete-instrument:"+instanceID+"/"+externalSymbol)
	return f.stateErr
}
func (f *fakeService) VerifyMarketDataSymbol(
	_ context.Context, id, externalSymbol string,
) (backend.MarketDataSymbolVerification, error) {
	f.mdCalls = append(f.mdCalls, "verify-symbol:"+id+"/"+externalSymbol)
	return f.mdVerify, f.mdVerifyErr
}
func (f *fakeService) SearchMarketDataSymbols(
	_ context.Context, id string, input backend.MarketDataSymbolSearchInput,
) (backend.MarketDataSymbolSearch, error) {
	f.mdCalls = append(f.mdCalls, "search-symbols:"+id+"/"+input.Query)
	f.mdSearchInput = input
	return f.mdSearch, f.mdSearchErr
}
func (f *fakeService) SetAccountGroup(
	_ context.Context, _ domain.AccountID, _ string,
	missing domain.MissingAccountPolicy,
) error {
	f.missingAccount = missing
	return f.stateErr
}
func (f *fakeService) SetAccountCurrency(
	_ context.Context, id domain.AccountID, currency string,
) error {
	for i, account := range f.accounts {
		if account.Code == id {
			f.accounts[i].Currency = currency
			return f.stateErr
		}
	}
	return f.stateErr
}
func (f *fakeService) SetAccountNotes(
	_ context.Context, code domain.AccountID, notes string,
) error {
	if f.stateErr != nil {
		return f.stateErr
	}
	f.accountNotesWrites = append(
		f.accountNotesWrites, accountNotesWrite{Code: code, Notes: notes},
	)
	return nil
}
func (f *fakeService) CreateGroup(
	_ context.Context, g domain.AccountGroup,
) (domain.AccountGroup, error) {
	if f.groupErr != nil {
		return domain.AccountGroup{}, f.groupErr
	}
	f.groups = append(f.groups, g)
	return g, nil
}
func (f *fakeService) UpdateGroup(
	_ context.Context, _ string, g domain.AccountGroup,
) (domain.AccountGroup, error) {
	if f.groupErr != nil {
		return domain.AccountGroup{}, f.groupErr
	}
	return g, nil
}
func (f *fakeService) ListGroups(_ context.Context) ([]domain.AccountGroup, error) {
	return f.groups, f.groupErr
}
func (f *fakeService) SetGroupCurrency(
	_ context.Context, code, currency string,
) error {
	for i, group := range f.groups {
		if group.Code == code {
			f.groups[i].Currency = currency
			return f.groupErr
		}
	}
	return f.groupErr
}
func (f *fakeService) SetDefaultGroupCurrency(_ context.Context, currency string) error {
	for i, group := range f.groups {
		if group.Code == "" {
			f.groups[i].Currency = currency
			return f.groupErr
		}
	}
	f.groups = append(f.groups, domain.AccountGroup{Code: "", Currency: currency})
	return f.groupErr
}
func (f *fakeService) ListGroupRows(
	_ context.Context, filter store.GroupListFilter,
) (store.GroupListPage, error) {
	f.groupFilter = filter
	if f.groupErr != nil {
		return store.GroupListPage{}, f.groupErr
	}
	if f.groupRows != nil {
		return store.GroupListPage{Rows: f.groupRows, Total: len(f.groupRows)}, nil
	}
	out := make([]store.GroupListRow, 0, len(f.groups))
	for _, group := range f.groups {
		out = append(out, store.GroupListRow{Group: group})
	}
	return store.GroupListPage{Rows: out, Total: len(out)}, nil
}
func (f *fakeService) GetGroup(
	_ context.Context, code string,
) (domain.AccountGroup, []domain.Account, error) {
	if f.groupErr != nil {
		return domain.AccountGroup{}, nil, f.groupErr
	}
	for _, g := range f.groups {
		if g.Code == code {
			return g, f.accounts, nil
		}
	}
	return domain.AccountGroup{}, nil, domain.ErrNotFound
}
func (f *fakeService) SetGroupNotes(_ context.Context, _, _ string) error {
	return f.groupErr
}
func (f *fakeService) SetGroupBlocked(_ context.Context, _ string, _ bool, _ string) error {
	return f.groupErr
}
func (f *fakeService) DeleteGroup(_ context.Context, _ string, force bool) error {
	f.deleteGroupForce = force
	return f.groupErr
}
func (f *fakeService) ApplyAdjustment(
	_ context.Context, _ domain.AccountID, externalID domain.ExternalID,
	_ domain.AdjustmentRequest, missing domain.MissingAccountPolicy,
) (domain.AccountAdjustmentRecord, error) {
	f.adjustmentExternalID = externalID
	f.missingAccount = missing
	return f.adjustment, f.stateErr
}
func (f *fakeService) SetBalanceRealizedPnl(
	_ context.Context, account domain.AccountID, asset string, realizedPnl string,
	missing domain.MissingAccountPolicy,
) (domain.Balance, error) {
	f.realizedPnlAsset = asset
	f.realizedPnlValue = realizedPnl
	f.missingAccount = missing
	if f.stateErr != nil {
		return domain.Balance{}, f.stateErr
	}
	balance := f.balanceRealizedPnl
	if balance.Account == "" {
		balance.Account = account
	}
	if balance.Asset == "" {
		balance.Asset = asset
	}
	if balance.RealizedPnl == "" {
		balance.RealizedPnl = realizedPnl
	}
	return balance, nil
}
func (f *fakeService) ListBalances(
	_ context.Context, _ domain.AccountID, _ string,
) ([]domain.Balance, error) {
	return f.balances, f.balancesErr
}

func (f *fakeService) ListBalanceRows(
	_ context.Context, filter store.BalanceListFilter,
) (store.BalanceListPage, error) {
	f.balanceFilter = filter
	if f.balancesErr != nil {
		return store.BalanceListPage{}, f.balancesErr
	}
	rows := make([]store.BalanceListRow, 0, len(f.balances))
	for _, balance := range f.balances {
		rows = append(rows, store.BalanceListRow{Balance: balance})
	}
	return store.BalanceListPage{Rows: rows, Total: len(rows)}, nil
}
func (f *fakeService) ListAdjustments(
	_ context.Context, _ domain.AccountID, _ domain.Source, _ int,
) ([]domain.AccountAdjustmentRecord, error) {
	return f.adjustments, f.stateErr
}
func (f *fakeService) ListAllAdjustments(
	_ context.Context, _ domain.AccountID, _ domain.Source, _ int,
) ([]domain.AccountAdjustmentRecord, error) {
	return f.adjustments, f.allAdjErr
}
func (f *fakeService) ListAdjustmentRows(
	_ context.Context, filter store.AdjustmentListFilter,
) (store.AdjustmentListPage, error) {
	f.adjustmentFilter = filter
	if f.allAdjErr != nil {
		return store.AdjustmentListPage{}, f.allAdjErr
	}
	if f.adjustmentPage != nil {
		return *f.adjustmentPage, nil
	}
	return store.AdjustmentListPage{Rows: f.adjustments, Total: len(f.adjustments)}, nil
}
func (f *fakeService) CheckOrder(_ context.Context, _ domain.OrderProbe) (domain.CheckResult, error) {
	return f.checkResult, f.stateErr
}
func (f *fakeService) ApplyExecutionReport(
	_ context.Context, in domain.ExecutionReportInput,
) (engine.ExecutionReportResult, backend.Attestation, error) {
	f.execReportIn = in
	if f.execReportErr != nil {
		return engine.ExecutionReportResult{}, backend.Attestation{}, f.execReportErr
	}
	return f.execReportResult, f.attestation, f.stateErr
}
func (f *fakeService) GetOrder(_ context.Context, _ string) (domain.OrderDetail, error) {
	return f.orderDetail, f.stateErr
}
func (f *fakeService) ListOrders(
	_ context.Context, _ domain.AccountID, _ domain.Source, _ int,
) ([]domain.Order, error) {
	return f.orders, f.ordersErr
}

func (f *fakeService) ListOrderRows(
	_ context.Context, filter store.OrderListFilter,
) (backend.OrderListPage, error) {
	f.orderFilter = filter
	if f.ordersErr != nil {
		return backend.OrderListPage{}, f.ordersErr
	}
	if f.orderPage != nil {
		return *f.orderPage, nil
	}
	rows := make([]backend.OrderListRow, 0, len(f.orders))
	for _, order := range f.orders {
		rows = append(rows, backend.OrderListRow{Order: order})
	}
	return backend.OrderListPage{Rows: rows, Total: len(rows)}, nil
}
func (f *fakeService) ListTrades(
	_ context.Context, _ domain.AccountID, _ domain.Source, _ int,
) ([]domain.Trade, error) {
	return f.trades, f.tradesErr
}
func (f *fakeService) ListTradeRows(
	_ context.Context, filter store.TradeListFilter,
) (store.TradeListPage, error) {
	f.tradeFilter = filter
	if f.tradesErr != nil {
		return store.TradeListPage{}, f.tradesErr
	}
	if f.tradePage != nil {
		return *f.tradePage, nil
	}
	return store.TradeListPage{Rows: f.trades, Total: len(f.trades)}, nil
}
func (f *fakeService) Overview(_ context.Context, _ time.Time) (backend.Overview, error) {
	return f.overview, f.statusErr
}
func (f *fakeService) ServiceInfo(_ context.Context) (backend.ServiceInfo, error) {
	return f.serviceInfo, f.statusErr
}
func (f *fakeService) RestartMarketData(_ context.Context) error {
	return f.stateErr
}
func (f *fakeService) GenerateSigningKey(_ context.Context) (domain.SigningKey, error) {
	return f.signingKey, f.signingErr
}
func (f *fakeService) ImportSigningKey(_ context.Context, _, _ string) (domain.SigningKey, error) {
	return f.signingKey, f.signingErr
}
func (f *fakeService) ListSigningKeys(_ context.Context) ([]domain.SigningKey, error) {
	return f.signingKeys, f.signingErr
}
func (f *fakeService) ActivePublicKey(format string) (string, error) {
	f.activePublicKeyFormat = format
	return f.activePublicKey, f.signingErr
}
func (f *fakeService) PublicKeyByID(_ context.Context, keyID, format string) (string, error) {
	f.keyByIDLast = keyID
	f.keyByIDFormat = format
	if f.signingErr != nil {
		return "", f.signingErr
	}
	if pub, ok := f.publicKeysByID[keyID]; ok {
		return pub, nil
	}
	return "", fmt.Errorf("signing: unknown keyId %q: %w", keyID, domain.ErrNotFound)
}
func (f *fakeService) GetNoESign(_ context.Context) (bool, error) {
	return f.noESign, f.signingErr
}
func (f *fakeService) SetNoESign(_ context.Context, off bool) error {
	f.noESignCalls++
	f.noESignSet = off
	return f.signingErr
}

// SubmitOrderToken mirrors the real backend: submit CREATES the order exactly
// once. It uses the caller-supplied external id when set, otherwise generates a
// deterministic one, records the created order keyed by that id, and returns an
// approval token whose OrderExternalID is the id actually used - so a later
// confirm/cancel resolves the same order. When signingErr is set it surfaces
// before any create, so duplicate/malformed-id rejection can be exercised.
func (f *fakeService) SubmitOrderToken(
	_ context.Context, o domain.Order, mode string,
	missing domain.MissingAccountPolicy,
) (backend.ApprovalToken, error) {
	f.submitTokenMode = mode
	f.submitOrderIn = o
	f.missingAccount = missing
	if f.signingErr != nil {
		return backend.ApprovalToken{}, f.signingErr
	}
	used := o.ExternalID
	if used.IsZero() {
		used = extID("generated-order")
	}
	o.ExternalID = used
	if f.submittedOrders == nil {
		f.submittedOrders = make(map[string]domain.Order)
	}
	f.submittedOrders[used.String()] = o
	tok := f.approvalToken
	tok.OrderExternalID = used.String()
	return tok, nil
}

func (f *fakeService) SubmitDropCopyOrder(
	ctx context.Context, o domain.Order, missing domain.MissingAccountPolicy,
) (domain.Order, error) {
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return domain.Order{}, err
	}
	f.missingAccount = missing
	o.DropCopy = true
	o.Source = caller.Source
	o.Principal = caller.Principal
	o.Status = domain.OrderStatusCommitted
	f.submitOrderIn = o
	if f.signingErr != nil {
		return domain.Order{}, f.signingErr
	}
	used := o.ExternalID
	if used.IsZero() {
		used = extID("generated-drop-copy-order")
	}
	o.ExternalID = used
	if f.submittedOrders == nil {
		f.submittedOrders = make(map[string]domain.Order)
	}
	f.submittedOrders[used.String()] = o
	return o, nil
}
func (f *fakeService) ConfirmExecution(
	_ context.Context, orderID string, _ string,
) (domain.Order, backend.Attestation, error) {
	if f.confirmErr != nil {
		return domain.Order{}, backend.Attestation{}, f.confirmErr
	}
	if f.signingErr != nil {
		return domain.Order{}, backend.Attestation{}, f.signingErr
	}
	if o, ok := f.submittedOrders[orderID]; ok {
		o.Status = domain.OrderStatusCommitted
		return o, f.attestation, nil
	}
	return f.submitOrder, f.attestation, nil
}
func (f *fakeService) CancelOrder(
	_ context.Context, orderID string, _, leavesQuantity, _ string,
) (domain.Order, backend.Attestation, error) {
	f.cancelCalls++
	f.cancelLeavesQuantity = leavesQuantity
	if f.cancelErr != nil {
		return domain.Order{}, backend.Attestation{}, f.cancelErr
	}
	if f.signingErr != nil {
		return domain.Order{}, backend.Attestation{}, f.signingErr
	}
	if o, ok := f.submittedOrders[orderID]; ok {
		o.Status = domain.OrderStatusRejected
		return o, f.attestation, nil
	}
	return f.submitOrder, f.attestation, nil
}

func (f *fakeService) CommandEnabled(context.Context, string) (bool, error) {
	return false, fmt.Errorf("fakeService: CommandEnabled is not part of the HTTP surface")
}
