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
	"time"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/businesscsv"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
)

// ControlPlane is the backend seam consumed by HTTP and extension handlers.
type ControlPlane interface {
	Status(ctx context.Context) (Status, error)
	ListAccounts(ctx context.Context) ([]domain.Account, error)
	ListAccountRows(
		ctx context.Context, filter store.AccountListFilter,
	) (store.AccountListPage, error)
	ExportBackup(ctx context.Context, scope backup.Scope) (backup.Archive, string, error)
	RestoreBackup(
		ctx context.Context,
		archive backup.Archive,
		opts backup.RestoreOptions,
	) (backup.RestoreSummary, error)
	ExportBusinessCSV(
		ctx context.Context,
		req BusinessCSVExportRequest,
	) (businesscsv.ExportFile, error)
	PreviewBusinessCSVImport(
		ctx context.Context,
		req BusinessCSVImportRequest,
	) (BusinessCSVImportPreview, error)
	ImportBusinessCSV(
		ctx context.Context,
		req BusinessCSVImportRequest,
	) (BusinessCSVImportResult, error)
	ResetDatabase(ctx context.Context) error
	ListAssets(ctx context.Context) ([]domain.Asset, error)
	ListAssetRows(
		ctx context.Context, filter store.AssetListFilter,
	) (store.AssetListPage, error)
	CreateAsset(ctx context.Context, asset domain.Asset) (domain.Asset, error)
	UpdateAsset(ctx context.Context, oldCode string, asset domain.Asset) (domain.Asset, error)
	DeleteAsset(ctx context.Context, code string, force bool) error
	ListAssetClasses(ctx context.Context) ([]domain.AssetClass, error)
	ListAssetClassRows(
		ctx context.Context, filter store.AssetClassListFilter,
	) (store.AssetClassListPage, error)
	CreateAssetClass(ctx context.Context, class domain.AssetClass) (domain.AssetClass, error)
	UpdateAssetClass(
		ctx context.Context, oldCode string, class domain.AssetClass,
	) (domain.AssetClass, error)
	DeleteAssetClass(ctx context.Context, code string, force bool) error
	CreateAccount(ctx context.Context, account domain.Account) (domain.Account, error)
	UpdateAccount(
		ctx context.Context,
		oldID domain.AccountID,
		account domain.Account,
	) (domain.Account, error)
	GetAccountState(ctx context.Context, id domain.AccountID) (domain.Account, node.AccountLimits, error)
	BlockAccount(ctx context.Context, id domain.AccountID, reason string) error
	UnblockAccount(ctx context.Context, id domain.AccountID) error
	DeleteAccount(ctx context.Context, id domain.AccountID, force bool) error
	SetAccountGroup(ctx context.Context, id domain.AccountID, groupCode string) error
	SetAccountNotes(ctx context.Context, id domain.AccountID, notes string) error
	ListLimits(ctx context.Context, account domain.AccountID) (node.AccountLimits, error)
	ListPolicyRows(
		ctx context.Context, filter store.PolicyListFilter,
	) (store.PolicyListPage, error)
	PutRateLimit(ctx context.Context, limit domain.LimitRate) error
	PutOrderSizeLimit(ctx context.Context, limit domain.LimitOrderSize) error
	PutPnlBoundsLimit(ctx context.Context, limit domain.LimitPnlBounds) error
	DeleteLimit(ctx context.Context, target node.LimitTarget) error
	ListAudit(ctx context.Context, count int) ([]domain.AuditRow, error)
	ListAuditFiltered(
		ctx context.Context, filter domain.AuditFilter, count int,
	) ([]domain.AuditRow, error)
	ListAuditRows(
		ctx context.Context, filter store.AuditListFilter,
	) (store.AuditListPage, error)
	ListMcpAccess(ctx context.Context) ([]McpCommand, error)
	SetMcpAccess(ctx context.Context, command string, enabled bool) error
	WelcomeSeen(ctx context.Context) (bool, error)
	SetWelcomeSeen(ctx context.Context, seen bool) error
	ListMarketData(ctx context.Context) (MarketDataStatus, error)
	RestartMarketData(ctx context.Context) error
	VerifyMarketDataSymbol(
		ctx context.Context, id, externalSymbol string,
	) (MarketDataSymbolVerification, error)
	SearchMarketDataSymbols(
		ctx context.Context, id string, input MarketDataSymbolSearchInput,
	) (MarketDataSymbolSearch, error)
	CreateMarketDataInstance(
		ctx context.Context, instance domain.MarketDataInstance,
	) (domain.MarketDataInstance, error)
	SetMarketDataInstanceEnabled(ctx context.Context, id string, enabled bool) error
	UpdateMarketDataInstanceSettings(
		ctx context.Context, id, label, credentials string,
	) error
	DeleteMarketDataInstance(ctx context.Context, id string, force bool) error
	UpsertMarketDataInstrument(ctx context.Context, instrument domain.MarketDataInstrument) error
	SetMarketDataInstrumentEnabled(
		ctx context.Context, instanceID, externalSymbol string, enabled bool,
	) error
	DeleteMarketDataInstrument(ctx context.Context, instanceID, externalSymbol string) error
	CreateGroup(ctx context.Context, group domain.AccountGroup) (domain.AccountGroup, error)
	UpdateGroup(
		ctx context.Context,
		oldCode string,
		group domain.AccountGroup,
	) (domain.AccountGroup, error)
	ListGroups(ctx context.Context) ([]domain.AccountGroup, error)
	ListGroupRows(
		ctx context.Context, filter store.GroupListFilter,
	) (store.GroupListPage, error)
	GetGroup(ctx context.Context, code string) (domain.AccountGroup, []domain.Account, error)
	SetGroupNotes(ctx context.Context, code, notes string) error
	SetGroupBlocked(ctx context.Context, code string, blocked bool, reason string) error
	DeleteGroup(ctx context.Context, code string) error
	ApplyAdjustment(
		ctx context.Context,
		account domain.AccountID,
		externalID domain.ExternalID,
		req domain.AdjustmentRequest,
	) (domain.AccountAdjustmentRecord, error)
	ListBalances(ctx context.Context, account domain.AccountID, asset string) ([]domain.Balance, error)
	ListBalanceRows(
		ctx context.Context, filter store.BalanceListFilter,
	) (store.BalanceListPage, error)
	ListAdjustments(
		ctx context.Context, account domain.AccountID, source domain.Source, n int,
	) ([]domain.AccountAdjustmentRecord, error)
	ListAllAdjustments(
		ctx context.Context, account domain.AccountID, source domain.Source, n int,
	) ([]domain.AccountAdjustmentRecord, error)
	ListAdjustmentRows(
		ctx context.Context, filter store.AdjustmentListFilter,
	) (store.AdjustmentListPage, error)
	SubmitOrder(ctx context.Context, o domain.Order) (domain.Order, error)
	CheckOrder(ctx context.Context, probe domain.OrderProbe) (domain.CheckResult, error)
	ApplyExecutionReport(
		ctx context.Context, in domain.ExecutionReportInput,
	) (engine.ExecutionReportResult, Attestation, error)
	GetOrder(ctx context.Context, id string) (domain.OrderDetail, error)
	ListOrders(
		ctx context.Context, account domain.AccountID, source domain.Source, n int,
	) ([]domain.Order, error)
	ListOrderRows(
		ctx context.Context, filter store.OrderListFilter,
	) (store.OrderListPage, error)
	ListTrades(
		ctx context.Context, account domain.AccountID, source domain.Source, n int,
	) ([]domain.Trade, error)
	ListTradeRows(
		ctx context.Context, filter store.TradeListFilter,
	) (store.TradeListPage, error)
	Overview(ctx context.Context, since time.Time) (Overview, error)
	ServiceInfo(ctx context.Context) (ServiceInfo, error)
	GenerateSigningKey(ctx context.Context) (domain.SigningKey, error)
	ImportSigningKey(ctx context.Context, material, format string) (domain.SigningKey, error)
	ListSigningKeys(ctx context.Context) ([]domain.SigningKey, error)
	ActivePublicKey(format string) (string, error)
	PublicKeyByID(ctx context.Context, keyID, format string) (string, error)
	GetNoESign(ctx context.Context) (bool, error)
	SetNoESign(ctx context.Context, off bool) error
	SubmitOrderToken(ctx context.Context, o domain.Order, mode string) (ApprovalToken, error)
	ConfirmExecution(
		ctx context.Context, orderID string, token string, force bool,
	) (domain.Order, Attestation, error)
	CancelOrder(
		ctx context.Context, orderID string, token, reason string, force bool,
	) (domain.Order, Attestation, error)
}
