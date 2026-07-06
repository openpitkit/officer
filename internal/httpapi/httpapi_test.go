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
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"testing/fstest"
	"time"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/businesscsv"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

// fakeService is a fake Service for handler tests.
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
	adjustments           []domain.AccountAdjustmentRecord
	adjustmentPage        *store.AdjustmentListPage
	adjustmentFilter      store.AdjustmentListFilter
	orders                []domain.Order
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
	csvPreview            backend.BusinessCSVImportPreview
	csvPreviewReq         backend.BusinessCSVImportRequest
	csvImport             backend.BusinessCSVImportResult
	csvImportReq          backend.BusinessCSVImportRequest
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
	assetClassErr         error
	createErr             error
	blockErr              error
	unblockErr            error
	stateErr              error
	execReportErr         error
	listLimErr            error
	putLimErr             error
	delLimErr             error
	auditErr              error
	groupErr              error

	// Captured typed-limit puts and delete target, for round-trip assertions.
	rateLimitPut      domain.LimitRate
	orderSizeLimitPut domain.LimitOrderSize
	pnlBoundsLimitPut domain.LimitPnlBounds
	deleteLimitTarget node.LimitTarget
	// Captured market-data instance create input.
	mdCreateInstance domain.MarketDataInstance

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
	approvalToken         backend.ApprovalToken
	attestation           backend.Attestation
	submitTokenMode       string
	confirmForce          bool
	cancelForce           bool
	signingErr            error
	// confirmErr/cancelErr inject a resolution failure (e.g. a terminal-order
	// conflict) into ConfirmExecution/CancelOrder, kept distinct from signingErr so
	// a test can drive the terminal-order path without touching the signing setup.
	confirmErr error
	cancelErr  error
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
func (f *fakeService) PreviewBusinessCSVImport(
	_ context.Context,
	req backend.BusinessCSVImportRequest,
) (backend.BusinessCSVImportPreview, error) {
	f.csvPreviewReq = req
	return f.csvPreview, f.csvErr
}
func (f *fakeService) ImportBusinessCSV(
	_ context.Context,
	req backend.BusinessCSVImportRequest,
) (backend.BusinessCSVImportResult, error) {
	f.csvImportReq = req
	return f.csvImport, f.csvErr
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
func (f *fakeService) CreateAccount(_ context.Context, account domain.Account) (domain.Account, error) {
	if f.createErr != nil {
		return domain.Account{}, f.createErr
	}
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
func (f *fakeService) BlockAccount(_ context.Context, _ domain.AccountID, _ string) error {
	return f.blockErr
}
func (f *fakeService) UnblockAccount(_ context.Context, _ domain.AccountID) error {
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
func (f *fakeService) PutRateLimit(_ context.Context, l domain.LimitRate) error {
	f.rateLimitPut = l
	return f.putLimErr
}
func (f *fakeService) PutOrderSizeLimit(_ context.Context, l domain.LimitOrderSize) error {
	f.orderSizeLimitPut = l
	return f.putLimErr
}
func (f *fakeService) PutPnlBoundsLimit(_ context.Context, l domain.LimitPnlBounds) error {
	f.pnlBoundsLimitPut = l
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
	_ context.Context, id string, force bool,
) error {
	f.mdCalls = append(f.mdCalls, fmt.Sprintf("delete-instance:%s:%v", id, force))
	return f.stateErr
}
func (f *fakeService) UpsertMarketDataInstrument(
	_ context.Context, instrument domain.MarketDataInstrument,
) error {
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
func (f *fakeService) SetAccountGroup(_ context.Context, _ domain.AccountID, _ string) error {
	return f.stateErr
}
func (f *fakeService) SetAccountNotes(_ context.Context, _ domain.AccountID, _ string) error {
	return f.stateErr
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
func (f *fakeService) DeleteGroup(_ context.Context, _ string) error {
	return f.groupErr
}
func (f *fakeService) ApplyAdjustment(
	_ context.Context, _ domain.AccountID, externalID domain.ExternalID,
	_ domain.AdjustmentRequest,
) (domain.AccountAdjustmentRecord, error) {
	f.adjustmentExternalID = externalID
	return f.adjustment, f.stateErr
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
func (f *fakeService) SubmitOrder(_ context.Context, _ domain.Order) (domain.Order, error) {
	return f.submitOrder, f.stateErr
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
	return engine.ExecutionReportResult{}, f.attestation, f.stateErr
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
) (store.OrderListPage, error) {
	f.orderFilter = filter
	if f.ordersErr != nil {
		return store.OrderListPage{}, f.ordersErr
	}
	rows := make([]store.OrderListRow, 0, len(f.orders))
	for _, order := range f.orders {
		rows = append(rows, store.OrderListRow{Order: order})
	}
	return store.OrderListPage{Rows: rows, Total: len(rows)}, nil
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
	f.noESignSet = off
	return f.signingErr
}

// SubmitOrderToken mirrors the real backend: submit CREATES the order exactly
// once. It uses the caller-supplied external id when set, otherwise generates a
// deterministic one, records the created order keyed by that id, and returns an
// approval token whose OrderExternalID is the id actually used — so a later
// confirm/cancel resolves the same order. When signingErr is set it surfaces
// before any create, so duplicate/malformed-id rejection can be exercised.
func (f *fakeService) SubmitOrderToken(
	_ context.Context, o domain.Order, mode string,
) (backend.ApprovalToken, error) {
	f.submitTokenMode = mode
	f.submitOrderIn = o
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
func (f *fakeService) ConfirmExecution(
	_ context.Context, orderID string, _ string, force bool,
) (domain.Order, backend.Attestation, error) {
	f.confirmForce = force
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
	_ context.Context, orderID string, _, _ string, force bool,
) (domain.Order, backend.Attestation, error) {
	f.cancelForce = force
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

// fakeSPA returns a minimal in-memory filesystem for the SPA option.
func fakeSPA() fs.FS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html></html>")},
	}
}

func newRouter(svc Service) (http.Handler, error) {
	return httpx.NewRouter(httpx.RouterConfig{
		Routes:      NewRouteRegistry(svc, nil),
		Authorizer:  httpx.AllowAll{},
		SPA:         fakeSPA(),
		BodyLimit:   BodyLimitPolicy(),
		ExtraMounts: ExtraMounts(),
	})
}

func TestRouteRegistrySurfaceBaseline(t *testing.T) {
	routes := NewRouteRegistry(&fakeService{}, nil).Routes()
	got := make([]string, 0, len(routes))
	for _, route := range routes {
		got = append(got, route.Method+" "+route.Pattern)
	}
	want := []string{
		"GET /health",
		"GET /status",
		"GET /service",
		"GET /overview",
		"POST /backup/export",
		"POST /backup/restore",
		"POST /business-csv/export",
		"POST /business-csv/import/preview",
		"POST /business-csv/import",
		"POST /database/reset",
		"GET /assets",
		"POST /assets",
		"PUT /assets/{code}",
		"DELETE /assets/{code}",
		"GET /asset-classes",
		"POST /asset-classes",
		"PUT /asset-classes/{code}",
		"DELETE /asset-classes/{code}",
		"GET /accounts",
		"POST /accounts",
		"GET /accounts/{code}",
		"PUT /accounts/{code}",
		"POST /accounts/{code}/block",
		"POST /accounts/{code}/unblock",
		"DELETE /accounts/{code}",
		"PUT /accounts/{code}/group",
		"PUT /accounts/{code}/notes",
		"GET /accounts/{code}/adjustments",
		"POST /accounts/{code}/adjustments",
		"GET /groups",
		"POST /groups",
		"GET /groups/{code}",
		"PUT /groups/{code}",
		"PUT /groups/{code}/notes",
		"POST /groups/{code}/block",
		"POST /groups/{code}/unblock",
		"DELETE /groups/{code}",
		"GET /balances",
		"GET /adjustments",
		"POST /orders",
		"POST /orders/check",
		"GET /orders",
		"GET /orders/{externalId}",
		"GET /orders/{externalId}/events/{eventId}/reproduction",
		"POST /orders/{externalId}/execution-reports",
		"GET /trades",
		"GET /limits",
		"PUT /limits/rate",
		"PUT /limits/order-size",
		"PUT /limits/pnl-bounds",
		"DELETE /limits",
		"GET /audit",
		"GET /audit/actions",
		"GET /mcp-access",
		"PUT /mcp-access/{command}",
		"GET /user-settings",
		"PUT /user-settings",
		"POST /signing/keys/generate",
		"POST /signing/keys/import",
		"GET /signing/keys",
		"GET /signing/keys/active/public",
		"GET /signing/keys/{keyId}/public",
		"GET /signing/config",
		"PUT /signing/config",
		"POST /orders/submit",
		"POST /orders/{externalId}/confirm",
		"POST /orders/{externalId}/cancel",
		"GET /market-data",
		"POST /market-data/restart",
		"POST /market-data/instances",
		"PUT /market-data/instances/{id}/enabled",
		"PUT /market-data/instances/{id}/settings",
		"DELETE /market-data/instances/{id}",
		"PUT /market-data/instances/{id}/instruments",
		"PUT /market-data/instances/{id}/instruments/enabled",
		"DELETE /market-data/instances/{id}/instruments",
		"POST /market-data/instances/{id}/verify-symbol",
		"POST /market-data/instances/{id}/search-symbols",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("route surface mismatch\ngot:  %v\nwant: %v", got, want)
	}
}

// bodyMap decodes a JSON response body into a map.
func bodyMap(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return m
}

// extID builds a deterministic ExternalID from a short seed for test fixtures.
// The wire form is Officer's generated external id for the 16 raw bytes.
func extID(seed string) domain.ExternalID {
	var b [16]byte
	copy(b[:], seed)
	id, err := domain.GeneratedExternalIDFromBytes(b[:])
	if err != nil {
		panic(err)
	}
	return id
}

// assertNoSurrogateID fails if a decoded response sub-map leaks any forbidden
// surrogate or engine identifier key. Machine records may expose their public
// opaque handle as "id"; numeric and engine ids must never be serialized.
func assertNoSurrogateID(t *testing.T, obj map[string]any) {
	t.Helper()
	if id, ok := obj["id"]; ok {
		if _, ok := id.(string); !ok {
			t.Fatalf("response leaked non-public id %q: %v", id, obj)
		}
	}
	for _, k := range []string{"orderId", "engineId", "engineAccountId", "engineGroupId"} {
		if _, ok := obj[k]; ok {
			t.Fatalf("response leaked forbidden key %q: %v", k, obj)
		}
	}
}

func TestHealthz(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestV1Health(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	if m["ok"] != true {
		t.Fatalf("want ok:true, got %v", m["ok"])
	}
}

func TestV1Status(t *testing.T) {
	svc := &fakeService{
		status: backend.Status{
			Nodes: []node.Health{{
				Engine: engine.Health{Version: "v1", Running: true},
				Store:  store.StoreHealth{Reachable: true},
			}},
			Healthy: true,
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	if m["healthy"] != true {
		t.Fatalf("want healthy:true, got %v", m["healthy"])
	}
}

func TestBusinessCSVExport(t *testing.T) {
	svc := &fakeService{csvExport: businesscsv.ExportFile{
		Name:        "pit-officer-accounts-20260625T100000Z.csv",
		ContentType: "text/csv; charset=utf-8",
		Body:        []byte("account_id,group_id\nacc-1,desk-a\n"),
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{
		"entity":"accounts",
		"delimiter":"pipe",
		"zip":true,
		"filters":{"groupCode":"desk-a","account":"acc-1","asset":"AAPL","source":"api"}
	}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/business-csv/export", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Disposition"); got !=
		`attachment; filename="pit-officer-accounts-20260625T100000Z.csv"` {
		t.Fatalf("content disposition = %q", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/csv; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}
	if svc.csvExportReq.Entity != businesscsv.EntityAccounts ||
		svc.csvExportReq.Delimiter != businesscsv.DelimiterPipe ||
		!svc.csvExportReq.Zip ||
		svc.csvExportReq.Filter.GroupCode != "desk-a" ||
		!svc.csvExportReq.Filter.GroupCodeSet ||
		svc.csvExportReq.Filter.Account != "acc-1" ||
		svc.csvExportReq.Filter.Asset != "AAPL" ||
		svc.csvExportReq.Filter.Source != domain.SourceAPI {
		t.Fatalf("csvExportReq = %+v", svc.csvExportReq)
	}
}

func TestBusinessCSVExportOmittedGroupFilter(t *testing.T) {
	svc := &fakeService{csvExport: businesscsv.ExportFile{
		Name:        "pit-officer-accounts-20260625T100000Z.csv",
		ContentType: "text/csv; charset=utf-8",
		Body:        []byte("account_id,group_id\n"),
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{
		"entity":"accounts",
		"delimiter":"comma",
		"filters":{"account":"acc-1"}
	}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/business-csv/export", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.csvExportReq.Filter.GroupCodeSet ||
		svc.csvExportReq.Filter.GroupCode != "" {
		t.Fatalf("csvExportReq = %+v", svc.csvExportReq)
	}
}

func TestBusinessCSVExportExplicitEmptyGroupFilter(t *testing.T) {
	svc := &fakeService{csvExport: businesscsv.ExportFile{
		Name:        "pit-officer-accounts-20260625T100000Z.csv",
		ContentType: "text/csv; charset=utf-8",
		Body:        []byte("account_id,group_id\n"),
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{
		"entity":"accounts",
		"delimiter":"comma",
		"filters":{"groupCode":""}
	}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/business-csv/export", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !svc.csvExportReq.Filter.GroupCodeSet ||
		svc.csvExportReq.Filter.GroupCode != "" {
		t.Fatalf("csvExportReq = %+v", svc.csvExportReq)
	}
}

func TestBusinessCSVImport(t *testing.T) {
	svc := &fakeService{csvImport: backend.BusinessCSVImportResult{
		Counts: businesscsv.ImportCounts{Rows: 1, Applied: 1},
		File:   businesscsv.ImportFile{Name: "accounts.csv", Type: "csv"},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	payload := base64.StdEncoding.EncodeToString([]byte(
		"account_id,group_id,notes,blocked,block_reason\nacc-1,,note,false,\n",
	))
	body := bytes.NewBufferString(fmt.Sprintf(`{
		"entity":"accounts",
		"delimiter":"comma",
		"filename":"accounts.csv",
		"payloadBase64":%q,
		"conflictPolicy":"replace"
	}`, payload))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/business-csv/import", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.csvImportReq.Entity != businesscsv.EntityAccounts ||
		svc.csvImportReq.Delimiter != businesscsv.DelimiterComma ||
		svc.csvImportReq.Filename != "accounts.csv" ||
		svc.csvImportReq.ConflictPolicy != businesscsv.ConflictReplace ||
		!bytes.Contains(svc.csvImportReq.Payload, []byte("acc-1")) {
		t.Fatalf("csvImportReq = %+v", svc.csvImportReq)
	}
}

func TestDecodeBusinessCSVPayloadBase64SizeGuard(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		encoded  string
		maxBytes int
		wantErr  bool
	}{
		{
			name:     "pre-decode too large",
			encoded:  "AAAAAAAAA",
			maxBytes: 4,
			wantErr:  true,
		},
		{
			name:     "post-decode too large",
			encoded:  base64.StdEncoding.EncodeToString([]byte("12345")),
			maxBytes: 4,
			wantErr:  true,
		},
		{
			name:     "at limit",
			encoded:  base64.StdEncoding.EncodeToString([]byte("1234")),
			maxBytes: 4,
			wantErr:  false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := decodeBusinessCSVPayloadBase64(tc.encoded, tc.maxBytes)
			if tc.wantErr {
				if !errors.Is(err, domain.ErrTooLarge) {
					t.Fatalf("error = %v, want too large", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeBusinessCSVPayloadBase64: %v", err)
			}
			if string(payload) != "1234" {
				t.Fatalf("payload = %q, want 1234", payload)
			}
		})
	}
}

func TestWriteErrMapsTooLargeTo413(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()

	httpx.WriteErr(rec, businesscsv.NewTooLargeError(false))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != "too_large" ||
		body.Error.Message == "" {
		t.Fatalf("error body = %+v", body.Error)
	}
}

func TestRequestBodyLimitUsesImportEnvelopeCap(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(
		http.MethodPost, "/api/v1/business-csv/import/preview", nil,
	)
	if got := BodyLimitPolicy()(req); got != maxImportBody {
		t.Fatalf("requestBodyLimit import = %d, want %d", got, maxImportBody)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/backup/restore", nil)
	if got := BodyLimitPolicy()(req); got != maxBackupRestoreBody {
		t.Fatalf("requestBodyLimit backup = %d, want %d", got, maxBackupRestoreBody)
	}
}

func TestListAccounts(t *testing.T) {
	svc := &fakeService{
		accountRows: []store.AccountListRow{
			{
				Account:       domain.Account{Code: "acc-1", Title: "Account One"},
				PositionCount: 2,
			},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/accounts", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	accounts, ok := m["accounts"].([]any)
	if !ok || len(accounts) != 1 {
		t.Fatalf("want 1 account, got %v", m["accounts"])
	}
	a := accounts[0].(map[string]any)
	// Accounts are addressed by their public code; no surrogate id leaks.
	if a["code"] != "acc-1" {
		t.Fatalf("want code=acc-1, got %v", a["code"])
	}
	if a["title"] != "Account One" {
		t.Fatalf("want title=Account One, got %v", a["title"])
	}
	assertNoSurrogateID(t, a)
	if _, ok := a["blocked"]; !ok {
		t.Fatal("missing blocked field")
	}
	if _, ok := a["blockReason"]; !ok {
		t.Fatal("missing blockReason field")
	}
	if a["positionCount"] != float64(2) {
		t.Fatalf("positionCount = %v, want 2", a["positionCount"])
	}
}

func TestListAccounts_PropagatesFilters(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/accounts?code=acc*alpha&codeMatch=starts_with"+
			"&status=blocked&positionCountMode=greater_than&positionCountMin=1"+
			"&blockReason=risk&blockReasonMatch=contains&group="+
			"&sort=positionCount&order=desc&limit=25&offset=50",
		nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if svc.accountFilter.GroupCode == nil || *svc.accountFilter.GroupCode != "" {
		t.Fatalf("group filter = %v, want empty group pointer", svc.accountFilter.GroupCode)
	}
	if got := svc.accountFilter.Code.Fragments; !slices.Equal(got, []string{"acc", "alpha"}) {
		t.Fatalf("code fragments = %v", got)
	}
	if !svc.accountFilter.Code.AnchorStart || svc.accountFilter.Code.AnchorEnd {
		t.Fatalf("code anchors = %+v, want start only", svc.accountFilter.Code)
	}
	if svc.accountFilter.Status != store.StatusFilterBlocked {
		t.Fatalf("status filter = %q", svc.accountFilter.Status)
	}
	if svc.accountFilter.Position.Min == nil || *svc.accountFilter.Position.Min != 1 ||
		!svc.accountFilter.Position.MinExclusive {
		t.Fatalf("position filter = %+v", svc.accountFilter.Position)
	}
	if got := svc.accountFilter.BlockReason.Fragments; !slices.Equal(got, []string{"risk"}) {
		t.Fatalf("block reason fragments = %v", got)
	}
	if svc.accountFilter.Sort.Column != "positionCount" ||
		!svc.accountFilter.Sort.Descending {
		t.Fatalf("sort filter = %+v", svc.accountFilter.Sort)
	}
	if svc.accountFilter.Page.Limit != 25 || svc.accountFilter.Page.Offset != 50 {
		t.Fatalf("page filter = %+v", svc.accountFilter.Page)
	}
}

func TestListAccounts_RejectsNotesSort(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/accounts?sort=notes",
		nil,
	))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestListBalances_PropagatesFilters(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/balances?account=acc&groupCode=desk"+
			"&asset=USD&availableMode=between&availableMin=2.5&availableMax=10"+
			"&updatedAtMode=greater_than&updatedAfter=2026-01-02T03:04:05Z"+
			"&sort=available&order=desc&limit=10&offset=20",
		nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if got := svc.balanceFilter.Account.Fragments; !slices.Equal(got, []string{"acc"}) {
		t.Fatalf("account fragments = %v", got)
	}
	if svc.balanceFilter.GroupCode == nil || *svc.balanceFilter.GroupCode != "desk" {
		t.Fatalf("group filter = %v", svc.balanceFilter.GroupCode)
	}
	if got := svc.balanceFilter.Asset.Fragments; !slices.Equal(got, []string{"USD"}) {
		t.Fatalf("asset fragments = %v", got)
	}
	if svc.balanceFilter.Available.Min == nil || svc.balanceFilter.Available.Max == nil {
		t.Fatalf("available range = %+v", svc.balanceFilter.Available)
	}
	if svc.balanceFilter.Available.MinExclusive || svc.balanceFilter.Available.MaxExclusive {
		t.Fatalf("available exclusivity = %+v", svc.balanceFilter.Available)
	}
	if svc.balanceFilter.UpdatedAt.Min == nil || !svc.balanceFilter.UpdatedAt.MinExclusive {
		t.Fatalf("updatedAt range = %+v", svc.balanceFilter.UpdatedAt)
	}
	if svc.balanceFilter.Sort.Column != "available" || !svc.balanceFilter.Sort.Descending {
		t.Fatalf("sort filter = %+v", svc.balanceFilter.Sort)
	}
	if svc.balanceFilter.Page.Limit != 10 || svc.balanceFilter.Page.Offset != 20 {
		t.Fatalf("page filter = %+v", svc.balanceFilter.Page)
	}
}

func TestListOrders_PropagatesFilters(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/orders?account=acc-1&source=panel&side=buy&status=accepted,filled"+
			"&baseAsset=A&quoteAsset=USD"+
			"&amountMode=greater_than&amountMin=2.5&priceMode=less_than&priceMax=10"+
			"&atMode=between&atMin=2026-01-02T03:04:05Z&atMax=2026-01-03T03:04:05Z"+
			"&sort=amountValue&order=asc&limit=5&offset=15",
		nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if svc.orderFilter.Account != "acc-1" || svc.orderFilter.Source != domain.SourcePanel {
		t.Fatalf("account/source = %q/%q", svc.orderFilter.Account, svc.orderFilter.Source)
	}
	if svc.orderFilter.Side == nil || *svc.orderFilter.Side != domain.OrderSideBuy {
		t.Fatalf("side filter = %v", svc.orderFilter.Side)
	}
	if !slices.Equal(svc.orderFilter.Status, []domain.OrderStatus{
		domain.OrderStatusAccepted,
		domain.OrderStatusFilled,
	}) {
		t.Fatalf("status filter = %v", svc.orderFilter.Status)
	}
	if got := svc.orderFilter.BaseAsset.Fragments; !slices.Equal(got, []string{"A"}) {
		t.Fatalf("base fragments = %v", got)
	}
	if got := svc.orderFilter.QuoteAsset.Fragments; !slices.Equal(got, []string{"USD"}) {
		t.Fatalf("quote fragments = %v", got)
	}
	if svc.orderFilter.Amount.Min == nil || !svc.orderFilter.Amount.MinExclusive {
		t.Fatalf("amount range = %+v", svc.orderFilter.Amount)
	}
	if svc.orderFilter.Price.Max == nil || !svc.orderFilter.Price.MaxExclusive {
		t.Fatalf("price range = %+v", svc.orderFilter.Price)
	}
	if svc.orderFilter.At.Min == nil || svc.orderFilter.At.Max == nil {
		t.Fatalf("at range = %+v", svc.orderFilter.At)
	}
	if svc.orderFilter.Sort.Column != "amountValue" || svc.orderFilter.Sort.Descending {
		t.Fatalf("sort filter = %+v", svc.orderFilter.Sort)
	}
	if svc.orderFilter.Page.Limit != 5 || svc.orderFilter.Page.Offset != 15 {
		t.Fatalf("page filter = %+v", svc.orderFilter.Page)
	}
}

func TestListOrders_PageParamComputesOffset(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "/api/v1/orders?limit=25&page=3", nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if svc.orderFilter.Page.Limit != 25 || svc.orderFilter.Page.Offset != 50 {
		t.Fatalf("page filter = %+v", svc.orderFilter.Page)
	}
}

func TestListOrders_OffsetParamWinsOverPage(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "/api/v1/orders?limit=25&page=3&offset=7", nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if svc.orderFilter.Page.Limit != 25 || svc.orderFilter.Page.Offset != 7 {
		t.Fatalf("page filter = %+v", svc.orderFilter.Page)
	}
}

func TestListOrders_BadPage(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "/api/v1/orders?page=0", nil,
	))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

func TestListTrades_PropagatesFilters(t *testing.T) {
	tradeID := extID("trade-exact")
	svc := &fakeService{
		tradePage: &store.TradeListPage{
			Total: 42,
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/trades?id="+tradeID.String()+
			"&account=acc*"+
			"&baseAsset=AAPL&quoteAsset=US"+
			"&side=buy&source=panel&atMode=between"+
			"&atMin=2026-01-02T03:04:05Z&atMax=2026-01-03T03:04:05Z"+
			"&quantityMode=gte&quantityMin=2.5&priceMode=lt&priceMax=150.75"+
			"&lockPriceMode=neq&lockPriceMin=149.5"+
			"&sort=quantity&order=desc&limit=7&offset=14",
		nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if got := svc.tradeFilter.Account.Fragments; !slices.Equal(got, []string{"acc*"}) {
		t.Fatalf("account fragments = %v", got)
	}
	if svc.tradeFilter.ExternalID != tradeID {
		t.Fatalf("external id = %s", svc.tradeFilter.ExternalID)
	}
	if !svc.tradeFilter.Account.AnchorStart || !svc.tradeFilter.Account.AnchorEnd {
		t.Fatalf("account matcher = %+v", svc.tradeFilter.Account)
	}
	if got := svc.tradeFilter.BaseAsset.Fragments; !slices.Equal(got, []string{"AAPL"}) {
		t.Fatalf("base fragments = %v", got)
	}
	if !svc.tradeFilter.BaseAsset.AnchorStart || !svc.tradeFilter.BaseAsset.AnchorEnd {
		t.Fatalf("base matcher = %+v", svc.tradeFilter.BaseAsset)
	}
	if got := svc.tradeFilter.QuoteAsset.Fragments; !slices.Equal(got, []string{"US"}) {
		t.Fatalf("quote fragments = %v", got)
	}
	if svc.tradeFilter.Side == nil || *svc.tradeFilter.Side != domain.OrderSideBuy {
		t.Fatalf("side = %v", svc.tradeFilter.Side)
	}
	if svc.tradeFilter.Source != domain.SourcePanel {
		t.Fatalf("source = %q", svc.tradeFilter.Source)
	}
	if svc.tradeFilter.At.Min == nil || svc.tradeFilter.At.Max == nil {
		t.Fatalf("at range = %+v", svc.tradeFilter.At)
	}
	if svc.tradeFilter.Quantity.Min == nil || *svc.tradeFilter.Quantity.Min != "2.5" ||
		svc.tradeFilter.Quantity.MinExclusive {
		t.Fatalf("quantity range = %+v", svc.tradeFilter.Quantity)
	}
	if svc.tradeFilter.Price.Max == nil || *svc.tradeFilter.Price.Max != "150.75" ||
		!svc.tradeFilter.Price.MaxExclusive {
		t.Fatalf("price range = %+v", svc.tradeFilter.Price)
	}
	if svc.tradeFilter.LockPrice.NotEqual == nil ||
		*svc.tradeFilter.LockPrice.NotEqual != "149.5" {
		t.Fatalf("lockPrice range = %+v", svc.tradeFilter.LockPrice)
	}
	if svc.tradeFilter.Sort.Column != "quantity" || !svc.tradeFilter.Sort.Descending {
		t.Fatalf("sort = %+v", svc.tradeFilter.Sort)
	}
	if svc.tradeFilter.Page.Limit != 7 || svc.tradeFilter.Page.Offset != 14 {
		t.Fatalf("page = %+v", svc.tradeFilter.Page)
	}
	m := bodyMap(t, rec.Result())
	if m["total"] != float64(42) {
		t.Fatalf("envelope = %v", m)
	}
}

func TestListAdjustments_PropagatesFilters(t *testing.T) {
	status := domain.AdjustmentStatusRejected
	svc := &fakeService{
		adjustmentPage: &store.AdjustmentListPage{
			Total: 17,
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/adjustments?id="+extID("adj-1").String()+
			"&account=acc-1&accountMatch=exact"+
			"&asset=US&assetMatch=starts_with&source=api&status=rejected"+
			"&atMode=gte&atMin=2026-01-02T03:04:05Z"+
			"&sort=status&order=asc&limit=9&offset=18",
		nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if svc.adjustmentFilter.ExternalID != extID("adj-1") {
		t.Fatalf("external id = %v", svc.adjustmentFilter.ExternalID)
	}
	if got := svc.adjustmentFilter.Account.Fragments; !slices.Equal(got, []string{"acc-1"}) {
		t.Fatalf("account fragments = %v", got)
	}
	if !svc.adjustmentFilter.Account.AnchorStart ||
		!svc.adjustmentFilter.Account.AnchorEnd {
		t.Fatalf("account matcher = %+v", svc.adjustmentFilter.Account)
	}
	if got := svc.adjustmentFilter.Asset.Fragments; !slices.Equal(got, []string{"US"}) {
		t.Fatalf("asset fragments = %v", got)
	}
	if !svc.adjustmentFilter.Asset.AnchorStart || !svc.adjustmentFilter.Asset.AnchorEnd {
		t.Fatalf("asset matcher = %+v", svc.adjustmentFilter.Asset)
	}
	if svc.adjustmentFilter.Source != domain.SourceAPI {
		t.Fatalf("source = %q", svc.adjustmentFilter.Source)
	}
	if svc.adjustmentFilter.Status == nil || *svc.adjustmentFilter.Status != status {
		t.Fatalf("status = %v", svc.adjustmentFilter.Status)
	}
	if svc.adjustmentFilter.At.Min == nil || svc.adjustmentFilter.At.MinExclusive {
		t.Fatalf("at range = %+v", svc.adjustmentFilter.At)
	}
	if svc.adjustmentFilter.Sort.Column != "status" ||
		svc.adjustmentFilter.Sort.Descending {
		t.Fatalf("sort = %+v", svc.adjustmentFilter.Sort)
	}
	if svc.adjustmentFilter.Page.Limit != 9 ||
		svc.adjustmentFilter.Page.Offset != 18 {
		t.Fatalf("page = %+v", svc.adjustmentFilter.Page)
	}
	m := bodyMap(t, rec.Result())
	if m["total"] != float64(17) {
		t.Fatalf("envelope = %v", m)
	}
}

func TestListAudit_PropagatesFilters(t *testing.T) {
	auditID := extID("audit-exact")
	svc := &fakeService{
		auditListPage: &store.AuditListPage{
			Total: 33,
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/audit?id="+auditID.String()+
			"&account=acc&accountMatch=starts_with"+
			"&asset=AAPL&assetMatch=exact"+
			"&actor=operator&actorMatch=exact&source=panel"+
			"&actions=block,unblock&atMode=lte&atMax=2026-01-03T03:04:05Z"+
			"&limit=4&offset=8",
		nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if got := svc.auditListFilter.Account.Fragments; !slices.Equal(got, []string{"acc"}) {
		t.Fatalf("account fragments = %v", got)
	}
	if svc.auditListFilter.ExternalID != auditID {
		t.Fatalf("external id = %s", svc.auditListFilter.ExternalID)
	}
	if !svc.auditListFilter.Account.AnchorStart || !svc.auditListFilter.Account.AnchorEnd {
		t.Fatalf("account matcher = %+v", svc.auditListFilter.Account)
	}
	if got := svc.auditListFilter.Asset.Fragments; !slices.Equal(got, []string{"AAPL"}) {
		t.Fatalf("asset fragments = %v", got)
	}
	if !svc.auditListFilter.Asset.AnchorStart || !svc.auditListFilter.Asset.AnchorEnd {
		t.Fatalf("asset matcher = %+v", svc.auditListFilter.Asset)
	}
	if got := svc.auditListFilter.Actor.Fragments; !slices.Equal(got, []string{"operator"}) {
		t.Fatalf("actor fragments = %v", got)
	}
	if !svc.auditListFilter.Actor.AnchorStart || !svc.auditListFilter.Actor.AnchorEnd {
		t.Fatalf("actor matcher = %+v", svc.auditListFilter.Actor)
	}
	if svc.auditListFilter.Source != domain.SourcePanel {
		t.Fatalf("source = %q", svc.auditListFilter.Source)
	}
	if !slices.Equal(svc.auditListFilter.Actions, []domain.AuditAction{
		domain.AuditActionBlock,
		domain.AuditActionUnblock,
	}) {
		t.Fatalf("actions = %v", svc.auditListFilter.Actions)
	}
	if svc.auditListFilter.At.Max == nil || svc.auditListFilter.At.MaxExclusive {
		t.Fatalf("at range = %+v", svc.auditListFilter.At)
	}
	if svc.auditListFilter.Page.Limit != 4 || svc.auditListFilter.Page.Offset != 8 {
		t.Fatalf("page = %+v", svc.auditListFilter.Page)
	}
	m := bodyMap(t, rec.Result())
	if m["total"] != float64(33) {
		t.Fatalf("envelope = %v", m)
	}
}

func TestListAssets(t *testing.T) {
	r, err := newRouter(&fakeService{
		assets: []domain.Asset{
			{Code: "AAPL", Title: "Apple Inc.", AssetClass: "equity"},
			{Code: "USD", Title: "US Dollar", AssetClass: "cash"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/assets", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	assets, ok := m["assets"].([]any)
	if !ok || len(assets) != 2 {
		t.Fatalf("want 2 assets, got %v", m["assets"])
	}
	if m["total"] != float64(2) {
		t.Fatalf("total = %v, want 2", m["total"])
	}
	first, _ := assets[0].(map[string]any)
	if first["code"] != "AAPL" || first["title"] != "Apple Inc." ||
		first["assetClass"] != "equity" {
		t.Fatalf("unexpected first asset: %v", first)
	}
}

func TestListAssets_PropagatesSort(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/assets?sort=assetClass&order=desc",
		nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if svc.assetFilter.Sort.Column != "assetClass" || !svc.assetFilter.Sort.Descending {
		t.Fatalf("sort = %+v", svc.assetFilter.Sort)
	}
}

func TestListAssets_PropagatesFilters(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/assets?code=apple&codeMatch=contains&class=equity&classMatch=exact&limit=5&offset=10",
		nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if len(svc.assetFilter.Code.Fragments) != 1 ||
		svc.assetFilter.Code.Fragments[0] != "apple" {
		t.Fatalf("code matcher = %+v", svc.assetFilter.Code)
	}
	if len(svc.assetFilter.Class.Fragments) != 1 ||
		svc.assetFilter.Class.Fragments[0] != "equity" ||
		!svc.assetFilter.Class.AnchorStart ||
		!svc.assetFilter.Class.AnchorEnd {
		t.Fatalf("class matcher = %+v", svc.assetFilter.Class)
	}
	if svc.assetFilter.Page.Limit != 5 || svc.assetFilter.Page.Offset != 10 {
		t.Fatalf("page = %+v", svc.assetFilter.Page)
	}
}

func TestListAssets_BadSort(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/assets?sort=bogus", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestCreateAsset(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"code":"AAPL","title":"Apple Inc.","assetClass":"equity"}`,
	)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/assets", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	asset, ok := m["asset"].(map[string]any)
	if !ok {
		t.Fatalf("want asset object, got %v", m["asset"])
	}
	if asset["code"] != "AAPL" || asset["title"] != "Apple Inc." ||
		asset["assetClass"] != "equity" {
		t.Fatalf("unexpected asset: %v", asset)
	}
	assertNoSurrogateID(t, asset)
}

func TestCreateAsset_ValidationError(t *testing.T) {
	svc := &fakeService{createAssetErr: domain.ErrInvalid}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"code":""}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/assets", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

func TestCreateAsset_Conflict(t *testing.T) {
	svc := &fakeService{createAssetErr: domain.ErrAlreadyExists}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"code":"AAPL"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/assets", body))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "conflict" {
		t.Fatalf("want code=conflict, got %v", errObj["code"])
	}
}

func TestUpdateAsset(t *testing.T) {
	r, err := newRouter(&fakeService{
		assets: []domain.Asset{{Code: "AAPL", Title: "Apple Inc.", AssetClass: "equity"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"code":"AAPL","title":"Apple","assetClass":"stock"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/assets/AAPL", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	asset, ok := m["asset"].(map[string]any)
	if !ok {
		t.Fatalf("want asset object, got %v", m["asset"])
	}
	if asset["code"] != "AAPL" || asset["title"] != "Apple" ||
		asset["assetClass"] != "stock" {
		t.Fatalf("unexpected asset: %v", asset)
	}
}

// TestUpdateAsset_Rename covers the code-edit path: the path code identifies the
// asset and the body carries a new public code, mirroring the group rename.
func TestUpdateAsset_Rename(t *testing.T) {
	svc := &fakeService{
		assets: []domain.Asset{{Code: "AAPL", Title: "Apple Inc."}},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"code":"AAPL.US","title":"Apple Inc."}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/assets/AAPL", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	asset, _ := m["asset"].(map[string]any)
	if asset["code"] != "AAPL.US" {
		t.Fatalf("unexpected renamed asset: %v", asset)
	}
	if svc.assets[0].Code != "AAPL.US" {
		t.Fatalf("asset not renamed in fake, got %v", svc.assets)
	}
}

func TestUpdateAsset_NotFound(t *testing.T) {
	svc := &fakeService{updateAssetErr: domain.ErrNotFound}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"title":"Apple"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/assets/AAPL", body))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestUpdateAsset_ValidationError(t *testing.T) {
	svc := &fakeService{updateAssetErr: domain.ErrInvalid}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"title":"Apple"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/assets/AAPL", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

func TestDeleteAsset(t *testing.T) {
	svc := &fakeService{
		assets: []domain.Asset{{Code: "AAPL", Title: "Apple Inc."}},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodDelete, "/api/v1/assets/AAPL?force=true", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
	if !svc.deleteAssetForce {
		t.Fatal("want force flag propagated to service")
	}
	if len(svc.assets) != 0 {
		t.Fatalf("want asset removed, got %v", svc.assets)
	}
}

func TestDeleteAsset_NotFound(t *testing.T) {
	svc := &fakeService{deleteAssetErr: domain.ErrNotFound}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/assets/AAPL", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestDeleteAsset_ValidationError(t *testing.T) {
	svc := &fakeService{deleteAssetErr: domain.ErrInvalid}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/assets/AAPL", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

func TestCreateAccount(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"code":"acc-1","title":"Account One"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/accounts", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	acc, ok := m["account"].(map[string]any)
	if !ok {
		t.Fatalf("want account object, got %v", m["account"])
	}
	if acc["code"] != "acc-1" {
		t.Fatalf("want code=acc-1, got %v", acc["code"])
	}
	if acc["title"] != "Account One" {
		t.Fatalf("want title=Account One, got %v", acc["title"])
	}
	assertNoSurrogateID(t, acc)
}

func TestCreateAccount_ValidationError(t *testing.T) {
	r, err := newRouter(&fakeService{createErr: domain.ErrInvalid})
	if err != nil {
		t.Fatal(err)
	}
	// Empty code fails ValidateAccountID
	body := bytes.NewBufferString(`{"code":""}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/accounts", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

func TestCreateAccount_Conflict(t *testing.T) {
	svc := &fakeService{createErr: domain.ErrAlreadyExists}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"code":"acc-1"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/accounts", body))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "conflict" {
		t.Fatalf("want code=conflict, got %v", errObj["code"])
	}
}

func TestVerifyMarketDataSymbol_Unsupported(t *testing.T) {
	svc := &fakeService{
		mdVerify: backend.MarketDataSymbolVerification{Supported: false},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"externalSymbol":"AAPL"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost,
		"/api/v1/market-data/instances/byo-1/verify-symbol",
		body,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	v, ok := m["verification"].(map[string]any)
	if !ok {
		t.Fatalf("want verification object, got %v", m["verification"])
	}
	if v["supported"] != false {
		t.Fatalf("want supported=false, got %v", v["supported"])
	}
	if len(svc.mdCalls) != 1 || svc.mdCalls[0] != "verify-symbol:byo-1/AAPL" {
		t.Fatalf("unexpected service calls: %v", svc.mdCalls)
	}
}

func TestVerifyMarketDataSymbol_SuggestionFlows(t *testing.T) {
	svc := &fakeService{
		mdVerify: backend.MarketDataSymbolVerification{
			Supported: true, Exists: false, Suggestion: "ETHUSDT",
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"externalSymbol":"ethusdt"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost,
		"/api/v1/market-data/instances/bn-1/verify-symbol",
		body,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	v, _ := m["verification"].(map[string]any)
	if v["supported"] != true || v["exists"] != false {
		t.Fatalf("want supported=true exists=false, got %v", v)
	}
	if v["suggestion"] != "ETHUSDT" {
		t.Fatalf("want suggestion=ETHUSDT, got %v", v["suggestion"])
	}
}

func TestGetAccount(t *testing.T) {
	svc := &fakeService{
		accounts: []domain.Account{
			{Code: "acc-1", Title: "Account One"},
		},
		limits: node.AccountLimits{
			RateLimits: []domain.LimitRate{
				{
					Scope:     domain.ScopeAccount,
					Account:   "acc-1",
					Window:    time.Second,
					MaxOrders: 100,
				},
			},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/accounts/acc-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	acc, ok := m["account"].(map[string]any)
	if !ok {
		t.Fatal("missing account field")
	}
	if acc["code"] != "acc-1" {
		t.Fatalf("want code=acc-1, got %v", acc["code"])
	}
	assertNoSurrogateID(t, acc)
	// The per-policy limits view carries the three typed barrier arrays.
	limits, ok := m["limits"].(map[string]any)
	if !ok {
		t.Fatalf("want limits object, got %v", m["limits"])
	}
	rates, _ := limits["rateLimits"].([]any)
	if len(rates) != 1 {
		t.Fatalf("want 1 rate limit, got %v", limits["rateLimits"])
	}
	rate := rates[0].(map[string]any)
	if rate["scope"] != domain.ScopeAccount || rate["account"] != "acc-1" ||
		rate["maxOrders"] != float64(100) || rate["windowMs"] != float64(1000) {
		t.Fatalf("unexpected rate limit: %v", rate)
	}
}

func TestGetAccount_NotFound(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/accounts/missing", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "not_found" {
		t.Fatalf("want code=not_found, got %v", errObj["code"])
	}
}

func TestGetAccount_URLEncodedID(t *testing.T) {
	// Verify that percent-encoded characters in the account id are decoded
	// before reaching the service. %40 = '@'.
	svc := &fakeService{
		accounts: []domain.Account{
			{Code: "acc@1"},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	// acc%401 decodes to acc@1
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/accounts/acc%401", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 for URL-encoded id, got %d", rec.Code)
	}
}

func TestBlockAccount(t *testing.T) {
	svc := &fakeService{
		accounts: []domain.Account{{Code: "acc-1"}},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"reason":"compliance"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/accounts/acc-1/block", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestBlockAccount_NotFound(t *testing.T) {
	svc := &fakeService{blockErr: domain.ErrNotFound}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"reason":"x"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/accounts/acc-x/block", body))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestUnblockAccount(t *testing.T) {
	svc := &fakeService{
		accounts: []domain.Account{{Code: "acc-1"}},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/accounts/acc-1/unblock", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestListLimits(t *testing.T) {
	svc := &fakeService{
		policyRows: []store.PolicyListRow{
			{
				Kind:  store.PolicyKindOrderSize,
				Scope: domain.ScopeBroker,
				OrderSize: &domain.LimitOrderSize{
					Scope: domain.ScopeBroker, MaxQuantity: "500",
				},
			},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/limits", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	// GET /limits returns the flat policy list plus a total.
	policies, ok := m["policies"].([]any)
	if !ok || len(policies) != 1 {
		t.Fatalf("want 1 policy, got %v", m["policies"])
	}
	if m["total"] != float64(1) {
		t.Fatalf("total = %v, want 1", m["total"])
	}
	policy := policies[0].(map[string]any)
	if policy["kind"] != domain.PolicyOrderSizeLimit || policy["scope"] != domain.ScopeBroker {
		t.Fatalf("unexpected policy: %v", policy)
	}
	values, ok := policy["values"].(map[string]any)
	if !ok {
		t.Fatalf("want values object, got %v", policy["values"])
	}
	orderSize, ok := values["orderSize"].(map[string]any)
	if !ok {
		t.Fatalf("want orderSize values, got %v", values)
	}
	if orderSize["maxQuantity"] != "500" {
		t.Fatalf("unexpected order-size values: %v", orderSize)
	}
}

func TestListLimits_FilterAndPaging(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/limits?account=acc-1&asset=AAPL&policy=rate&sort=scope&order=desc&limit=20&offset=40",
		nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	// Account is matched exactly (anchored both ends, single fragment).
	if got := svc.policyFilter.Account.Fragments; len(got) != 1 || got[0] != "acc-1" {
		t.Fatalf("account fragments = %v", got)
	}
	if !svc.policyFilter.Account.AnchorStart || !svc.policyFilter.Account.AnchorEnd {
		t.Fatalf("account not exact-anchored: %+v", svc.policyFilter.Account)
	}
	if got := svc.policyFilter.Asset.Fragments; len(got) != 1 || got[0] != "AAPL" {
		t.Fatalf("asset fragments = %v", got)
	}
	if !svc.policyFilter.Asset.AnchorStart || !svc.policyFilter.Asset.AnchorEnd {
		t.Fatalf("asset not exact-anchored: %+v", svc.policyFilter.Asset)
	}
	if svc.policyFilter.Kind == nil || *svc.policyFilter.Kind != store.PolicyKindRate {
		t.Fatalf("kind filter = %v", svc.policyFilter.Kind)
	}
	if svc.policyFilter.Sort.Column != "scope" || !svc.policyFilter.Sort.Descending {
		t.Fatalf("sort spec = %+v", svc.policyFilter.Sort)
	}
	if svc.policyFilter.Page.Limit != 20 || svc.policyFilter.Page.Offset != 40 {
		t.Fatalf("page spec = %+v", svc.policyFilter.Page)
	}
}

func TestListLimits_BadSort(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/limits?sort=unknown", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestListLimits_BadPolicy(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/limits?policy=invalid", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestListLimits_BadLimit(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/limits?limit=-1", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestPutRateLimit(t *testing.T) {
	svc := &fakeService{
		limits: node.AccountLimits{
			RateLimits: []domain.LimitRate{
				{
					Scope:     domain.ScopeAccountAsset,
					Account:   "acc-1",
					Asset:     "AAPL",
					Window:    2 * time.Second,
					MaxOrders: 101,
				},
			},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{
		"scope":"account_asset","account":"acc-1","asset":"AAPL",
		"windowMs":1000,"maxOrders":100
	}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/limits/rate", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	// The handler captures the typed barrier and responds from persisted state.
	if svc.rateLimitPut.Scope != domain.ScopeAccountAsset ||
		svc.rateLimitPut.Account != "acc-1" ||
		svc.rateLimitPut.Asset != "AAPL" ||
		svc.rateLimitPut.Window != time.Second ||
		svc.rateLimitPut.MaxOrders != 100 {
		t.Fatalf("captured rate limit = %+v", svc.rateLimitPut)
	}
	m := bodyMap(t, rec.Result())
	rl, ok := m["rateLimit"].(map[string]any)
	if !ok {
		t.Fatalf("response missing rateLimit field: %v", m)
	}
	if rl["maxOrders"] != float64(101) || rl["windowMs"] != float64(2000) {
		t.Fatalf("unexpected persisted rateLimit: %v", rl)
	}
}

func TestPutOrderSizeLimit(t *testing.T) {
	svc := &fakeService{
		limits: node.AccountLimits{
			OrderSizeLimits: []domain.LimitOrderSize{
				{
					Scope:       domain.ScopeAccountAsset,
					Account:     "acc-1",
					Asset:       "AAPL",
					MaxQuantity: "501",
					MaxNotional: "50001",
				},
			},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{
		"scope":"account_asset","account":"acc-1","asset":"AAPL",
		"maxQuantity":"500","maxNotional":"50000"
	}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/limits/order-size", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.orderSizeLimitPut.Scope != domain.ScopeAccountAsset ||
		svc.orderSizeLimitPut.Account != "acc-1" ||
		svc.orderSizeLimitPut.Asset != "AAPL" ||
		svc.orderSizeLimitPut.MaxQuantity != "500" ||
		svc.orderSizeLimitPut.MaxNotional != "50000" {
		t.Fatalf("captured order-size limit = %+v", svc.orderSizeLimitPut)
	}
	m := bodyMap(t, rec.Result())
	osl, ok := m["orderSizeLimit"].(map[string]any)
	if !ok {
		t.Fatalf("response missing orderSizeLimit field: %v", m)
	}
	if osl["maxQuantity"] != "501" || osl["maxNotional"] != "50001" {
		t.Fatalf("unexpected persisted orderSizeLimit: %v", osl)
	}
}

func TestPutPnlBoundsLimit(t *testing.T) {
	svc := &fakeService{
		limits: node.AccountLimits{
			PnlBoundsLimits: []domain.LimitPnlBounds{
				{
					Scope:      domain.ScopeAccountAsset,
					Account:    "acc-1",
					Asset:      "AAPL",
					LowerBound: "-999",
					UpperBound: "5001",
					InitialPnl: "1",
				},
			},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{
		"scope":"account_asset","account":"acc-1","asset":"AAPL",
		"lowerBound":"-1000","upperBound":"5000","initialPnl":"0"
	}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/limits/pnl-bounds", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.pnlBoundsLimitPut.Scope != domain.ScopeAccountAsset ||
		svc.pnlBoundsLimitPut.Account != "acc-1" ||
		svc.pnlBoundsLimitPut.Asset != "AAPL" ||
		svc.pnlBoundsLimitPut.LowerBound != "-1000" ||
		svc.pnlBoundsLimitPut.UpperBound != "5000" ||
		svc.pnlBoundsLimitPut.InitialPnl != "0" {
		t.Fatalf("captured pnl-bounds limit = %+v", svc.pnlBoundsLimitPut)
	}
	m := bodyMap(t, rec.Result())
	pbl, ok := m["pnlBoundsLimit"].(map[string]any)
	if !ok {
		t.Fatalf("response missing pnlBoundsLimit field: %v", m)
	}
	if pbl["lowerBound"] != "-999" || pbl["upperBound"] != "5001" ||
		pbl["initialPnl"] != "1" {
		t.Fatalf("unexpected persisted pnlBoundsLimit: %v", pbl)
	}
}

func TestPutRateLimit_ValidationError(t *testing.T) {
	svc := &fakeService{putLimErr: fmt.Errorf("bad scope: %w", domain.ErrInvalid)}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"scope":"broker","windowMs":1000,"maxOrders":100}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/limits/rate", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestPutRateLimit_NotImplemented(t *testing.T) {
	// A wrapped domain.ErrNotImplemented (the engine's not-implemented stub
	// surfacing through the node and backend) maps to HTTP 501 with the wrapped
	// message, taking precedence over the generic 500 path.
	const msg = "engine: rate_limit add not supported yet"
	svc := &fakeService{
		putLimErr: fmt.Errorf("%s: %w", msg, domain.ErrNotImplemented),
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"scope":"asset","asset":"AAPL","windowMs":1000,"maxOrders":100}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/limits/rate", body))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("want 501, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "not_implemented" {
		t.Fatalf("want code=not_implemented, got %v", errObj["code"])
	}
	if errObj["message"] != fmt.Sprintf("%s: %s", msg, domain.ErrNotImplemented) {
		t.Fatalf("want wrapped message, got %v", errObj["message"])
	}
}

func TestPutRateLimit_EngineRestarting(t *testing.T) {
	const msg = "engine restart in progress; mutating requests are rejected until rebuild completes"
	svc := &fakeService{
		putLimErr: fmt.Errorf("%s: %w", msg, domain.ErrEngineRestarting),
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"scope":"asset","asset":"AAPL","windowMs":1000,"maxOrders":100}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/limits/rate", body))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "engine_restarting" {
		t.Fatalf("want code=engine_restarting, got %v", errObj["code"])
	}
	if errObj["message"] != fmt.Sprintf("%s: %s", msg, domain.ErrEngineRestarting) {
		t.Fatalf("want wrapped message, got %v", errObj["message"])
	}
}

func TestDeleteLimit(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete,
		"/api/v1/limits?policy=rate_limit&scope=account_asset&account=acc-1&asset=AAPL", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
	// The delete target round-trips into the typed node.LimitTarget.
	if svc.deleteLimitTarget.Policy != domain.PolicyRateLimit ||
		svc.deleteLimitTarget.Scope != domain.ScopeAccountAsset ||
		svc.deleteLimitTarget.Account != "acc-1" ||
		svc.deleteLimitTarget.Asset != "AAPL" {
		t.Fatalf("captured delete target = %+v", svc.deleteLimitTarget)
	}
}

func TestDeleteLimit_NotFound(t *testing.T) {
	svc := &fakeService{delLimErr: domain.ErrNotFound}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete,
		"/api/v1/limits?policy=rate_limit&scope=broker", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

// TestDeleteAccount_HasDependents asserts the 409 has_dependents wire shape: a
// HasDependentsError from the service maps to HTTP 409 with body
// error.code == "has_dependents" and a dependents array of {kind, count}.
func TestDeleteAccount_HasDependents(t *testing.T) {
	svc := &fakeService{stateErr: domain.NewHasDependentsError([]domain.DependentCount{
		{Kind: "orders", Count: 3},
	})}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/accounts/acc-1", nil))
	assertHasDependents409(t, rec, "orders", 3)
}

// TestDeleteMarketDataInstance_HasDependents asserts the same 409 wire shape for
// the market-data instance delete endpoint.
func TestDeleteMarketDataInstance_HasDependents(t *testing.T) {
	svc := &fakeService{stateErr: domain.NewHasDependentsError([]domain.DependentCount{
		{Kind: "instruments", Count: 2},
	})}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete,
		"/api/v1/market-data/instances/inst-1", nil))
	assertHasDependents409(t, rec, "instruments", 2)
}

// assertHasDependents409 checks the recorded response is the 409 has_dependents
// wire shape with a single dependent of the wanted kind/count.
func assertHasDependents409(t *testing.T, rec *httptest.ResponseRecorder, wantKind string, wantCount int) {
	t.Helper()
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	body := bodyMap(t, rec.Result())
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("missing error object: %+v", body)
	}
	if errObj["code"] != "has_dependents" {
		t.Fatalf("error.code = %v, want has_dependents", errObj["code"])
	}
	deps, ok := errObj["dependents"].([]any)
	if !ok || len(deps) != 1 {
		t.Fatalf("dependents = %v, want one entry", errObj["dependents"])
	}
	dep, ok := deps[0].(map[string]any)
	if !ok {
		t.Fatalf("dependent[0] not an object: %v", deps[0])
	}
	if dep["kind"] != wantKind {
		t.Fatalf("dependent kind = %v, want %q", dep["kind"], wantKind)
	}
	// JSON numbers decode to float64.
	if count, _ := dep["count"].(float64); int(count) != wantCount {
		t.Fatalf("dependent count = %v, want %d", dep["count"], wantCount)
	}
}

func TestListAudit(t *testing.T) {
	ts := time.Date(2026, 6, 11, 10, 0, 0, 1, time.UTC)
	svc := &fakeService{
		auditRows: []domain.AuditRow{
			{
				ExternalID: extID("audit-1"),
				At:         ts,
				Actor:      "operator",
				Action:     domain.AuditActionSetLimit,
				Account:    "acc-1",
				Detail:     "set limit rate_limit account=acc-1",
			},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/audit", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	entries, _ := m["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %v", m["entries"])
	}
	e := entries[0].(map[string]any)
	for _, field := range []string{"externalId", "at", "actor", "action", "account", "detail"} {
		if _, ok := e[field]; !ok {
			t.Fatalf("audit entry missing field %q", field)
		}
	}
	// The audit row is addressed by its opaque external id; no surrogate id leaks.
	if e["externalId"] != extID("audit-1").String() {
		t.Fatalf("want externalId=%s, got %v", extID("audit-1").String(), e["externalId"])
	}
	assertNoSurrogateID(t, e)
	if e["actor"] != "operator" {
		t.Fatalf("want actor=operator, got %v", e["actor"])
	}
}

func TestListAudit_FilterResolution(t *testing.T) {
	cases := []struct {
		name         string
		query        string
		wantActions  []domain.AuditAction
		wantCategory domain.AuditCategory
	}{
		{"default unfiltered", "/api/v1/audit", nil, ""},
		{"category control", "/api/v1/audit?category=control", nil, domain.AuditCategoryControl},
		{"category trading", "/api/v1/audit?category=trading", nil, domain.AuditCategoryTrading},
		{"category all", "/api/v1/audit?category=all", nil, ""},
		{"explicit actions", "/api/v1/audit?actions=block,submit_order",
			[]domain.AuditAction{domain.AuditActionBlock, domain.AuditActionSubmitOrder}, ""},
		{"explicit actions override category", "/api/v1/audit?category=control&actions=submit_order",
			[]domain.AuditAction{domain.AuditActionSubmitOrder}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.query, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("want 200, got %d", rec.Code)
			}
			if !slices.Equal(svc.auditListFilter.Actions, tc.wantActions) {
				t.Fatalf("actions = %+v, want %+v", svc.auditListFilter.Actions, tc.wantActions)
			}
			if svc.auditListFilter.Category != tc.wantCategory {
				t.Fatalf("category = %q, want %q", svc.auditListFilter.Category, tc.wantCategory)
			}
		})
	}
}

func TestListAudit_UnknownActionRejected(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/audit?actions=bogus", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for unknown action, got %d", rec.Code)
	}
}

func TestListAudit_BadQueryParams(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{"unknown_sort", "?sort=unknown"},
		{"invalid_order", "?sort=source&order=sideways"},
		{"invalid_category", "?category=bogus"},
		{"invalid_time", "?atMode=after&atMin=not-time"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := newRouter(&fakeService{})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
				"/api/v1/audit"+tc.query, nil))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d", rec.Code)
			}
			m := bodyMap(t, rec.Result())
			errObj, _ := m["error"].(map[string]any)
			if errObj["code"] != "validation" {
				t.Fatalf("want code=validation, got %v", errObj["code"])
			}
		})
	}
}

func TestListAudit_LimitCapping(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	// Requesting more than auditCapREST should be silently capped (not error).
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/v1/audit?limit=%d", auditCapREST+999), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestListAudit_InvalidLimit(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/audit?limit=abc", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestListAuditActions(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/audit/actions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	groups, ok := m["groups"].([]any)
	if !ok || len(groups) != 2 {
		t.Fatalf("want 2 groups, got %v", m["groups"])
	}

	// auditGroup decodes one group entry into its category and ordered actions.
	auditGroup := func(v any) (string, []string) {
		g := v.(map[string]any)
		category, _ := g["category"].(string)
		raw, _ := g["actions"].([]any)
		actions := make([]string, 0, len(raw))
		for _, a := range raw {
			actions = append(actions, a.(string))
		}
		return category, actions
	}

	controlCat, controlActions := auditGroup(groups[0])
	tradingCat, tradingActions := auditGroup(groups[1])
	if controlCat != string(domain.AuditCategoryControl) {
		t.Fatalf("groups[0].category = %q, want control", controlCat)
	}
	if tradingCat != string(domain.AuditCategoryTrading) {
		t.Fatalf("groups[1].category = %q, want trading", tradingCat)
	}

	wantControl := auditActionStrings(
		domain.AuditActionsByCategory(domain.AuditCategoryControl))
	wantTrading := auditActionStrings(
		domain.AuditActionsByCategory(domain.AuditCategoryTrading))
	if !slices.Equal(controlActions, wantControl) {
		t.Fatalf("control actions = %v, want %v", controlActions, wantControl)
	}
	if !slices.Equal(tradingActions, wantTrading) {
		t.Fatalf("trading actions = %v, want %v", tradingActions, wantTrading)
	}

	// The trading group is exactly the high-volume order/execution stream.
	if !slices.Equal(tradingActions, []string{"submit_order", "execution_report"}) {
		t.Fatalf("trading group = %v, want [submit_order execution_report]", tradingActions)
	}

	// The concatenation of all groups equals the full canonical catalogue in
	// order; this guards against future drift between the grouped endpoint and
	// domain.AllAuditActions.
	got := append(append([]string{}, controlActions...), tradingActions...)
	want := auditActionStrings(domain.AllAuditActions())
	if !slices.Equal(got, want) {
		t.Fatalf("concatenated actions = %v, want %v", got, want)
	}
}

func TestLimitDTO_JSONShape(t *testing.T) {
	// The per-policy limits view marshals into the three typed barrier arrays
	// with the camelCase wire keys the contract requires.
	limits := node.AccountLimits{
		RateLimits: []domain.LimitRate{
			{Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "AAPL",
				Window: time.Second, MaxOrders: 100},
		},
		OrderSizeLimits: []domain.LimitOrderSize{
			{Scope: domain.ScopeBroker, MaxQuantity: "500", MaxNotional: "50000"},
		},
		PnlBoundsLimits: []domain.LimitPnlBounds{
			{Scope: domain.ScopeAsset, Asset: "AAPL",
				LowerBound: "-1000", UpperBound: "5000", InitialPnl: "0"},
		},
	}
	b, err := json.Marshal(toAccountLimitsDTO(limits))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	rates, _ := m["rateLimits"].([]any)
	if len(rates) != 1 {
		t.Fatalf("want 1 rate limit, got %v", m["rateLimits"])
	}
	rate := rates[0].(map[string]any)
	for _, key := range []string{"scope", "account", "asset", "windowMs", "maxOrders"} {
		if _, ok := rate[key]; !ok {
			t.Fatalf("rateLimitDTO missing JSON key %q", key)
		}
	}
	if rate["maxOrders"] != float64(100) || rate["windowMs"] != float64(1000) {
		t.Fatalf("unexpected rate limit: %v", rate)
	}
	sizes, _ := m["orderSizeLimits"].([]any)
	size := sizes[0].(map[string]any)
	for _, key := range []string{"scope", "account", "asset", "maxQuantity", "maxNotional"} {
		if _, ok := size[key]; !ok {
			t.Fatalf("orderSizeLimitDTO missing JSON key %q", key)
		}
	}
	pnls, _ := m["pnlBoundsLimits"].([]any)
	pnl := pnls[0].(map[string]any)
	for _, key := range []string{"scope", "account", "asset", "lowerBound", "upperBound", "initialPnl"} {
		if _, ok := pnl[key]; !ok {
			t.Fatalf("pnlBoundsLimitDTO missing JSON key %q", key)
		}
	}
}

func TestAuditDTO_JSONShape(t *testing.T) {
	ts := time.Date(2026, 6, 11, 10, 0, 0, 1, time.UTC)
	row := domain.AuditRow{
		ExternalID: extID("audit-1"), At: ts, Actor: "operator",
		Action: domain.AuditActionSetLimit, Account: "acc-1",
		Detail: "set limit rate_limit asset=AAPL max_orders=100 window=1s",
	}
	b, err := json.Marshal(toAuditDTO(row))
	if err != nil {
		t.Fatal(err)
	}
	// Verify the at field is RFC3339Nano format.
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	atStr, _ := m["at"].(string)
	// time.Time marshals as RFC3339Nano; verify it parses back to the
	// same instant.
	got, err := time.Parse(time.RFC3339Nano, atStr)
	if err != nil {
		t.Fatalf("at field not RFC3339Nano: %v", atStr)
	}
	if !got.Equal(ts) {
		t.Fatalf("at round-trip mismatch: want %v, got %v", ts, got)
	}
}

func TestNewRouter_MissingService(t *testing.T) {
	_, err := httpx.NewRouter(httpx.RouterConfig{
		Authorizer: httpx.AllowAll{},
		SPA:        fakeSPA(),
		BodyLimit:  BodyLimitPolicy(),
	})
	if err == nil {
		t.Fatal("want error for nil route registry")
	}
}

func TestNewRouter_MissingSPA(t *testing.T) {
	_, err := httpx.NewRouter(httpx.RouterConfig{
		Routes:     NewRouteRegistry(&fakeService{}, nil),
		Authorizer: httpx.AllowAll{},
		BodyLimit:  BodyLimitPolicy(),
	})
	if err == nil {
		t.Fatal("want error for nil SPA")
	}
}

func TestCheckOrder_Pass(t *testing.T) {
	svc := &fakeService{checkResult: domain.CheckResult{
		Passed: true, WouldLockPrices: []string{"100"},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"account":"acc-1","baseAsset":"AAPL","quoteAsset":"USD","side":"buy","amountKind":"quantity","amountValue":"1","price":"100"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/orders/check", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	check, ok := m["check"].(map[string]any)
	if !ok {
		t.Fatalf("want check object, got %v", m["check"])
	}
	if check["passed"] != true {
		t.Fatalf("want passed=true, got %v", check["passed"])
	}
	prices, ok := check["wouldDisplayPrices"].([]any)
	if !ok || len(prices) != 1 || prices[0] != "100" {
		t.Fatalf("want wouldDisplayPrices=[100], got %v", check["wouldDisplayPrices"])
	}
	if check["wouldBlock"] != nil {
		t.Fatalf("want wouldBlock=null, got %v", check["wouldBlock"])
	}
}

func TestCheckOrder_Reject(t *testing.T) {
	svc := &fakeService{checkResult: domain.CheckResult{
		Passed: false,
		Rejects: []domain.OrderReject{
			{Code: "insufficient_funds", Scope: "account", Policy: "spot_funds", Reason: "no funds"},
		},
		WouldBlock: &domain.ExecutionAccountBlock{Account: "acc-1", Code: "account_blocked"},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"account":"acc-1","baseAsset":"AAPL","quoteAsset":"USD","side":"buy","amountKind":"quantity","amountValue":"1"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/orders/check", body))
	// An engine reject is a successful 200, not an HTTP error.
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	check, _ := m["check"].(map[string]any)
	if check["passed"] != false {
		t.Fatalf("want passed=false, got %v", check["passed"])
	}
	rejects, ok := check["rejects"].([]any)
	if !ok || len(rejects) != 1 {
		t.Fatalf("want 1 reject, got %v", check["rejects"])
	}
	rej, _ := rejects[0].(map[string]any)
	if rej["code"] != "insufficient_funds" || rej["scope"] != "account" {
		t.Fatalf("reject fields not on the wire: %v", rej)
	}
	block, ok := check["wouldBlock"].(map[string]any)
	if !ok || block["account"] != "acc-1" || block["code"] != "account_blocked" {
		t.Fatalf("wouldBlock not on the wire: %v", check["wouldBlock"])
	}
}

func TestCheckOrder_InvalidJSON(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/orders/check", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestCheckOrder_ValidationError(t *testing.T) {
	// The backend rejects a malformed account/asset with ErrInvalid; the handler
	// maps it to 400, mirroring submit.
	svc := &fakeService{stateErr: domain.ErrInvalid}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"account":"acc-1","baseAsset":"bad asset","quoteAsset":"USD","side":"buy","amountKind":"quantity","amountValue":"1"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/orders/check", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

// TestSubmitOrder_Created checks the submit path returns 201: the order row is
// persisted on every success path (even an engine reject), so the resource-
// creating POST is a 201 Created carrying the order.
func TestSubmitOrder_Created(t *testing.T) {
	svc := &fakeService{submitOrder: domain.Order{
		ExternalID: extID("order-1"), Account: "acc-1", BaseAsset: "AAPL",
		QuoteAsset: "USD", Side: domain.OrderSideBuy,
		Status: domain.OrderStatusCommitted,
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"account":"acc-1","baseAsset":"AAPL","quoteAsset":"USD","side":"buy","amountKind":"quantity","amountValue":"1","price":"100"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/orders", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	if _, ok := m["order"].(map[string]any); !ok {
		t.Fatalf("want order object, got %v", m["order"])
	}
}

// TestSubmitOrder_ValidationError checks malformed order input (a bad enum,
// decimal, or asset the engine mapper rejects with domain.ErrInvalid) surfaces
// as 400, not the 500 default. The fake stands in for the engine mapper raising
// ErrInvalid for, e.g., an unknown amount kind or a non-decimal amount.
func TestSubmitOrder_ValidationError(t *testing.T) {
	svc := &fakeService{stateErr: domain.ErrInvalid}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"account":"acc-1","baseAsset":"AAPL","quoteAsset":"USD","side":"buy","amountKind":"base","amountValue":"1"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/orders", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

// TestApplyAdjustment_Created checks the adjustment path returns 201: the
// adjustment record is appended on every success path (accept or reject), so the
// resource-creating POST is a 201 Created carrying the record.
func TestApplyAdjustment_Created(t *testing.T) {
	svc := &fakeService{adjustment: domain.AccountAdjustmentRecord{
		ExternalID: extID("adj-1"), Account: "acc-1",
		Request: domain.AdjustmentRequest{Asset: "USD"},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"asset":"USD","balance":{"mode":"delta","value":"100"}}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/accounts/acc-1/adjustments", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	adj, ok := m["adjustment"].(map[string]any)
	if !ok {
		t.Fatalf("want adjustment object, got %v", m["adjustment"])
	}
	if adj["externalId"] != extID("adj-1").String() {
		t.Fatalf("want externalId=%s, got %v", extID("adj-1").String(), adj["externalId"])
	}
	assertNoSurrogateID(t, adj)
}

func TestApplyAdjustment_NoChangeReturnsNoContent(t *testing.T) {
	svc := &fakeService{stateErr: domain.ErrNoChange}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"asset":"USD","balance":{"mode":"delta","value":"0"}}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/accounts/acc-1/adjustments", body))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("response body = %q, want empty", rec.Body.String())
	}
}

// TestApplyExecutionReport_Created checks the execution-report path returns 201:
// the trade row is created on the success path, so the resource-creating POST is
// a 201 Created carrying the result.
func TestApplyExecutionReport_Created(t *testing.T) {
	svc := &fakeService{orderDetail: domain.OrderDetail{
		Order: domain.Order{
			ExternalID: extID("order-1"), Account: "acc-1", BaseAsset: "AAPL",
			QuoteAsset: "USD", Side: domain.OrderSideBuy,
		},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"quantity":"1","price":"100","leavesQuantity":"0","status":"cancelled","force":true}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/orders/"+extID("order-1").String()+"/execution-reports", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	if _, ok := m["result"].(map[string]any); !ok {
		t.Fatalf("want result object, got %v", m["result"])
	}
	if !svc.execReportIn.Force {
		t.Fatal("force was not forwarded to ApplyExecutionReport")
	}
	if svc.execReportIn.LeavesQuantity != "0" {
		t.Fatalf("leavesQuantity not forwarded: %q", svc.execReportIn.LeavesQuantity)
	}
	if svc.execReportIn.OrderStatus != domain.OrderStatusCancelled {
		t.Fatalf("status not forwarded: %q", svc.execReportIn.OrderStatus)
	}
	if svc.execReportIn.Account != "" ||
		svc.execReportIn.BaseAsset != "" ||
		svc.execReportIn.QuoteAsset != "" ||
		svc.execReportIn.Side != "" {
		t.Fatalf("order-derived fields were populated by HTTP: %+v", svc.execReportIn)
	}
}

// TestApplyExecutionReport_MissingLeavesQuantity checks the handler rejects a
// fill body that omits leavesQuantity with 400 validation, locking in the
// engine's hard requirement that every report carries leaves before it reaches
// the service.
func TestApplyExecutionReport_MissingLeavesQuantity(t *testing.T) {
	svc := &fakeService{orderDetail: domain.OrderDetail{
		Order: domain.Order{
			ExternalID: extID("order-1"), Account: "acc-1", BaseAsset: "AAPL",
			QuoteAsset: "USD", Side: domain.OrderSideBuy,
		},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"quantity":"1","price":"100","status":"filled"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/orders/"+extID("order-1").String()+"/execution-reports", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

// TestApplyExecutionReport_NonFillMissingLeaves checks a no-trade lifecycle
// report without leavesQuantity is rejected with 400: leaves is required on
// every report, not only fills.
func TestApplyExecutionReport_NonFillMissingLeaves(t *testing.T) {
	svc := &fakeService{orderDetail: domain.OrderDetail{
		Order: domain.Order{
			ExternalID: extID("order-1"), Account: "acc-1", BaseAsset: "AAPL",
			QuoteAsset: "USD", Side: domain.OrderSideBuy,
		},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"status":"cancelled"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/orders/"+extID("order-1").String()+"/execution-reports", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

// TestApplyExecutionReport_NonFillForwardsLeaves checks a no-trade lifecycle
// report that carries leavesQuantity is forwarded verbatim to the service.
func TestApplyExecutionReport_NonFillForwardsLeaves(t *testing.T) {
	svc := &fakeService{orderDetail: domain.OrderDetail{
		Order: domain.Order{
			ExternalID: extID("order-1"), Account: "acc-1", BaseAsset: "AAPL",
			QuoteAsset: "USD", Side: domain.OrderSideBuy,
		},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"status":"cancelled","leavesQuantity":"0"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/orders/"+extID("order-1").String()+"/execution-reports", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rec.Code)
	}
	if svc.execReportIn.OrderStatus != domain.OrderStatusCancelled {
		t.Fatalf("status not forwarded: %q", svc.execReportIn.OrderStatus)
	}
	if svc.execReportIn.LeavesQuantity != "0" {
		t.Fatalf("leavesQuantity not forwarded: %q", svc.execReportIn.LeavesQuantity)
	}
}

func TestApplyExecutionReport_InvalidStatus(t *testing.T) {
	svc := &fakeService{orderDetail: domain.OrderDetail{
		Order: domain.Order{
			ExternalID: extID("order-1"), Account: "acc-1", BaseAsset: "AAPL",
			QuoteAsset: "USD", Side: domain.OrderSideBuy,
		},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"quantity":"1","price":"100","leavesQuantity":"0","status":"done"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/orders/"+extID("order-1").String()+"/execution-reports", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
	if svc.execReportIn.Order != "" {
		t.Fatalf("invalid status reached service: %+v", svc.execReportIn)
	}
}

func TestApplyExecutionReport_MissingStatus(t *testing.T) {
	svc := &fakeService{orderDetail: domain.OrderDetail{
		Order: domain.Order{
			ExternalID: extID("order-1"), Account: "acc-1", BaseAsset: "AAPL",
			QuoteAsset: "USD", Side: domain.OrderSideBuy,
		},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"quantity":"1","price":"100","leavesQuantity":"0"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/orders/"+extID("order-1").String()+"/execution-reports", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

func TestListMcpAccess(t *testing.T) {
	svc := &fakeService{
		mcpCommands: []backend.McpCommand{
			{Command: backend.Command{
				Name: "health", Title: "Health", AgentDescription: "desc",
				Implemented: true, DefaultEnabled: true,
			}, Enabled: false},
			{Command: backend.Command{
				Name: "set_limit", Title: "Set limit", AgentDescription: "desc",
				Mutating: true, Protective: true,
			}, Enabled: true},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/mcp-access", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	commands, ok := m["commands"].([]any)
	if !ok || len(commands) != 2 {
		t.Fatalf("want 2 commands, got %v", m["commands"])
	}
	first := commands[0].(map[string]any)
	if first["name"] != "health" || first["enabled"] != false || first["implemented"] != true {
		t.Fatalf("unexpected first command: %v", first)
	}
}

func TestSetMcpAccess_Persists(t *testing.T) {
	svc := &fakeService{
		mcpCommands: []backend.McpCommand{
			{Command: backend.Command{Name: "health", Title: "Health", AgentDescription: "d"}, Enabled: false},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"enabled":false}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/mcp-access/health", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if len(svc.setMcpCalls) != 1 || svc.setMcpCalls[0].command != "health" ||
		svc.setMcpCalls[0].enabled != false {
		t.Fatalf("expected persisted toggle, got %+v", svc.setMcpCalls)
	}
	m := bodyMap(t, rec.Result())
	cmd, ok := m["command"].(map[string]any)
	if !ok || cmd["name"] != "health" {
		t.Fatalf("expected command in body, got %v", m)
	}
}

func TestSetMcpAccess_UnknownCommandNotFound(t *testing.T) {
	svc := &fakeService{setMcpErr: fmt.Errorf("mcp command %q: %w", "nope", domain.ErrNotFound)}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"enabled":true}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/mcp-access/nope", body))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestSetMcpAccess_InvalidJSON(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{bad`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/mcp-access/health", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	if len(svc.setMcpCalls) != 0 {
		t.Fatalf("invalid JSON must not persist")
	}
}

func TestUserSettings_GetAndPut(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}

	// Fresh store reports the welcome dialog as not yet dismissed.
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/user-settings", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET want 200, got %d", rec.Code)
	}
	if m := bodyMap(t, rec.Result()); m["welcomeSeen"] != false {
		t.Fatalf("GET welcomeSeen = %v, want false", m["welcomeSeen"])
	}

	// Persisting the choice round-trips through the service.
	body := bytes.NewBufferString(`{"welcomeSeen":true}`)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/user-settings", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT want 200, got %d", rec.Code)
	}
	if !svc.welcomeSeen {
		t.Fatalf("PUT did not persist welcomeSeen")
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/user-settings", nil))
	if m := bodyMap(t, rec.Result()); m["welcomeSeen"] != true {
		t.Fatalf("GET after PUT welcomeSeen = %v, want true", m["welcomeSeen"])
	}
}

func TestUserSettings_PutInvalidJSON(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{bad`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/user-settings", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}
