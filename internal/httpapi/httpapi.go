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

// Package httpapi is Pit Officer's HTTP surface for the `serve` run mode. It
// serves three things behind one chi router: a liveness probe (/healthz), the
// REST control-plane API (/api/v1/*), the streamable-HTTP MCP handler (/mcp),
// and the embedded single-page operator dashboard with an SPA fallback for
// client-side routes.
//
// The wire shape of /api/v1 is the dashboard's contract. Domain types carry no
// JSON tags; this package owns the explicit camelCase response DTOs and the
// mapping from domain types onto them.
package httpapi

import (
	"context"
	"net/http"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/store"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

// auditCapREST is the maximum number of audit rows the REST endpoint returns.
const auditCapREST = 1000

// maxRequestBody bounds ordinary v1 request bodies. Most control-plane bodies
// are tiny (a handful of decimal strings or flags), so 1 MiB is generous.
const maxRequestBody = 1 << 20

// maxBackupRestoreBody bounds the one route that accepts an uploaded backup
// archive. Keeping this separate avoids widening every v1 endpoint's body cap.
const maxBackupRestoreBody = 64 << 20

// listDefaultLimit is the default page size for the orders, trades, and
// adjustments list endpoints; listCapREST bounds an explicit ?limit=.
const (
	listDefaultLimit = 100
	listCapREST      = 1000
)

// Service is the control-plane seam the HTTP surface calls into.
type Service interface {
	backend.ControlPlane
	ListAdjustmentRows(context.Context, store.AdjustmentListFilter) (store.AdjustmentListPage, error)
	ListTradeRows(context.Context, store.TradeListFilter) (store.TradeListPage, error)
	ListAuditRows(context.Context, store.AuditListFilter) (store.AuditListPage, error)
}

// RegisterRoutes registers the open app's v1 route table into registry.
func RegisterRoutes(registry *httpx.RouteRegistry, svc Service, logs httpx.LogSource) {
	register(registry, "health.get", http.MethodGet, "/health", handleV1Health)
	register(registry, "status.get", http.MethodGet, "/status", handleV1Status(svc))
	register(registry, "service.get", http.MethodGet, "/service", handleServiceInfo(svc))
	// The log tail is optional: only the serve path supplies it, so the routes
	// register only when a source is present.
	if logs != nil {
		register(registry, "service.logs.get", http.MethodGet,
			"/service/logs", handleServiceLogs(logs))
		register(registry, "service.logs.download.get", http.MethodGet,
			"/service/logs/download", handleServiceLogsDownload(logs))
	}
	register(registry, "overview.get", http.MethodGet, "/overview", handleOverview(svc))

	register(registry, "backup.export.post", http.MethodPost, "/backup/export", handleExportBackup(svc))
	register(registry, "backup.restore.post", http.MethodPost, "/backup/restore", handleRestoreBackup(svc))
	register(registry, "business-csv.export.post", http.MethodPost, "/business-csv/export", handleExportBusinessCSV(svc))
	register(registry, "database.reset.post", http.MethodPost, "/database/reset", handleResetDatabase(svc))

	register(registry, "assets.list.get", http.MethodGet, "/assets", handleListAssets(svc))
	register(registry, "assets.create.post", http.MethodPost, "/assets", handleCreateAsset(svc))
	register(registry, "assets.update.put", http.MethodPut, "/assets/{code}", handleUpdateAsset(svc))
	register(registry, "assets.delete.delete", http.MethodDelete, "/assets/{code}", handleDeleteAsset(svc))

	register(registry, "asset_classes.list.get", http.MethodGet, "/asset-classes", handleListAssetClasses(svc))
	register(registry, "asset_classes.create.post", http.MethodPost, "/asset-classes", handleCreateAssetClass(svc))
	register(registry, "asset_classes.update.put", http.MethodPut, "/asset-classes/{code}", handleUpdateAssetClass(svc))
	register(registry, "asset_classes.delete.delete", http.MethodDelete, "/asset-classes/{code}", handleDeleteAssetClass(svc))

	register(registry, "accounts.list.get", http.MethodGet, "/accounts", handleListAccounts(svc))
	register(registry, "accounts.create.post", http.MethodPost, "/accounts", handleCreateAccount(svc))
	register(registry, "accounts.get", http.MethodGet, "/accounts/{code}", handleGetAccount(svc))
	register(registry, "accounts.update.put", http.MethodPut, "/accounts/{code}", handleUpdateAccount(svc))
	register(registry, "accounts.block.post", http.MethodPost, "/accounts/{code}/block", handleBlockAccount(svc))
	register(registry, "accounts.unblock.post", http.MethodPost, "/accounts/{code}/unblock", handleUnblockAccount(svc))
	register(registry, "accounts.delete", http.MethodDelete, "/accounts/{code}", handleDeleteAccount(svc))
	register(registry, "accounts.group.put", http.MethodPut, "/accounts/{code}/group", handleSetAccountGroup(svc))
	register(registry, "accounts.currency.put", http.MethodPut, "/accounts/{code}/currency", handleSetAccountCurrency(svc))
	register(registry, "accounts.notes.put", http.MethodPut, "/accounts/{code}/notes", handleSetAccountNotes(svc))
	register(registry, "accounts.adjustments.list.get", http.MethodGet, "/accounts/{code}/adjustments", handleListAccountAdjustments(svc))
	register(registry, "accounts.adjustments.apply.post", http.MethodPost, "/accounts/{code}/adjustments", handleApplyAdjustment(svc))
	register(registry, "accounts.balances.realized_pnl.put", http.MethodPut, "/accounts/{code}/balances/realized-pnl", handleSetBalanceRealizedPnl(svc))

	register(registry, "groups.list.get", http.MethodGet, "/groups", handleListGroups(svc))
	register(registry, "groups.create.post", http.MethodPost, "/groups", handleCreateGroup(svc))
	register(registry, "groups.default.currency.put", http.MethodPut, "/groups/-/default/currency", handleSetDefaultGroupCurrency(svc))
	register(registry, "groups.get", http.MethodGet, "/groups/{code}", handleGetGroup(svc))
	register(registry, "groups.update.put", http.MethodPut, "/groups/{code}", handleUpdateGroup(svc))
	register(registry, "groups.currency.put", http.MethodPut, "/groups/{code}/currency", handleSetGroupCurrency(svc))
	register(registry, "groups.notes.put", http.MethodPut, "/groups/{code}/notes", handleSetGroupNotes(svc))
	register(registry, "groups.block.post", http.MethodPost, "/groups/{code}/block", handleBlockGroup(svc))
	register(registry, "groups.unblock.post", http.MethodPost, "/groups/{code}/unblock", handleUnblockGroup(svc))
	register(registry, "groups.delete", http.MethodDelete, "/groups/{code}", handleDeleteGroup(svc))

	register(registry, "balances.list.get", http.MethodGet, "/balances", handleListBalances(svc))
	register(registry, "adjustments.list.get", http.MethodGet, "/adjustments", handleListAdjustments(svc))

	register(registry, "orders.check.post", http.MethodPost, "/orders/check", handleCheckOrder(svc))
	register(registry, "orders.list.get", http.MethodGet, "/orders", handleListOrders(svc))
	register(registry, "orders.get", http.MethodGet, "/orders/{id}", handleGetOrder(svc))
	register(registry, "orders.events.reproduction.get", http.MethodGet, "/orders/{id}/events/{eventId}/reproduction", handleGetOrderEventReproduction(svc))
	register(registry, "orders.execution-reports.post", http.MethodPost, "/orders/{id}/execution-reports", handleApplyExecutionReport(svc))
	register(registry, "trades.list.get", http.MethodGet, "/trades", handleListTrades(svc))

	register(registry, "limits.list.get", http.MethodGet, "/limits", handleListLimits(svc))
	register(registry, "limits.rate.put", http.MethodPut, "/limits/rate", handlePutRateLimit(svc))
	register(registry, "limits.order-size.put", http.MethodPut, "/limits/order-size", handlePutOrderSizeLimit(svc))
	register(registry, "limits.spot-funds-pnl-bounds.put", http.MethodPut, "/limits/spot-funds-pnl-bounds", handlePutSpotFundsPnlBoundsLimit(svc))
	register(registry, "limits.delete", http.MethodDelete, "/limits", handleDeleteLimit(svc))

	register(registry, "audit.list.get", http.MethodGet, "/audit", handleListAudit(svc))
	register(registry, "audit.actions.get", http.MethodGet, "/audit/actions", handleListAuditActions())

	register(registry, "mcp-access.list.get", http.MethodGet, "/mcp-access", handleListMcpAccess(svc))
	register(registry, "mcp-access.set.put", http.MethodPut, "/mcp-access/{command}", handleSetMcpAccess(svc))

	register(registry, "user-settings.get", http.MethodGet, "/user-settings", handleGetUserSettings(svc))
	register(registry, "user-settings.put", http.MethodPut, "/user-settings", handleSetUserSettings(svc))

	register(registry, "signing.keys.generate.post", http.MethodPost, "/signing/keys/generate", handleGenerateSigningKey(svc))
	register(registry, "signing.keys.import.post", http.MethodPost, "/signing/keys/import", handleImportSigningKey(svc))
	register(registry, "signing.keys.list.get", http.MethodGet, "/signing/keys", handleListSigningKeys(svc))
	register(registry, "signing.keys.active-public.get", http.MethodGet, "/signing/keys/active/public", handleGetActivePublicKey(svc))
	register(registry, "signing.keys.public.get", http.MethodGet, "/signing/keys/{keyId}/public", handleGetSigningKeyPublic(svc))
	register(registry, "signing.config.get", http.MethodGet, "/signing/config", handleGetSigningConfig(svc))
	register(registry, "signing.config.put", http.MethodPut, "/signing/config", handleSetSigningConfig(svc))
	register(registry, "orders.submit-token.post", http.MethodPost, "/orders/submit", handleSubmitOrderToken(svc))
	register(registry, "orders.submit-drop-copy.post", http.MethodPost, "/orders/drop-copy/submit", handleSubmitDropCopyOrder(svc))
	register(registry, "orders.confirm.post", http.MethodPost, "/orders/{id}/confirm", handleConfirmExecution(svc))
	register(registry, "orders.cancel.post", http.MethodPost, "/orders/{id}/cancel", handleCancelOrder(svc))

	register(registry, "market-data.list.get", http.MethodGet, "/market-data", handleListMarketData(svc))
	register(registry, "market-data.restart.post", http.MethodPost, "/market-data/restart", handleRestartMarketData(svc))
	register(registry, "market-data.instances.create.post", http.MethodPost, "/market-data/instances", handleCreateMarketDataInstance(svc))
	register(registry, "market-data.instances.enabled.put", http.MethodPut, "/market-data/instances/{id}/enabled", handleSetMarketDataInstanceEnabled(svc))
	register(registry, "market-data.instances.settings.put", http.MethodPut, "/market-data/instances/{id}/settings", handleUpdateMarketDataInstanceSettings(svc))
	register(registry, "market-data.instances.delete", http.MethodDelete, "/market-data/instances/{id}", handleDeleteMarketDataInstance(svc))
	register(registry, "market-data.instruments.upsert.put", http.MethodPut, "/market-data/instances/{id}/instruments", handleUpsertMarketDataInstrument(svc))
	register(registry, "market-data.instruments.enabled.put", http.MethodPut, "/market-data/instances/{id}/instruments/enabled", handleSetMarketDataInstrumentEnabled(svc))
	register(registry, "market-data.instruments.delete", http.MethodDelete, "/market-data/instances/{id}/instruments", handleDeleteMarketDataInstrument(svc))
	register(registry, "market-data.verify-symbol.post", http.MethodPost, "/market-data/instances/{id}/verify-symbol", handleVerifyMarketDataSymbol(svc))
	register(registry, "market-data.search-symbols.post", http.MethodPost, "/market-data/instances/{id}/search-symbols", handleSearchMarketDataSymbols(svc))
}

// NewRouteRegistry builds the open app's v1 route registry.
func NewRouteRegistry(svc Service, logs httpx.LogSource) *httpx.RouteRegistry {
	registry := &httpx.RouteRegistry{}
	RegisterRoutes(registry, svc, logs)
	return registry
}

// BodyLimitPolicy returns the open app's per-path request body cap policy.
func BodyLimitPolicy() func(*http.Request) int64 {
	return httpx.BodyLimitPolicy(maxRequestBody, map[string]int64{
		"/api/v1/backup/restore":     maxBackupRestoreBody,
		"/app/api/v1/backup/restore": maxBackupRestoreBody,
	})
}

// ExtraMounts returns the open app's non-v1 HTTP routes.
func ExtraMounts() []httpx.ExtraMount {
	return []httpx.ExtraMount{
		{
			Method:  http.MethodGet,
			Pattern: "/healthz",
			Handler: http.HandlerFunc(handleHealthz),
		},
		{
			Method:  http.MethodGet,
			Pattern: "/api/openapi.yaml",
			Handler: http.HandlerFunc(serveOpenAPISpec),
		},
		{
			Method:  http.MethodGet,
			Pattern: "/docs",
			Handler: http.HandlerFunc(serveSwaggerUI),
		},
	}
}

func register(
	registry *httpx.RouteRegistry,
	id string,
	method string,
	pattern string,
	handler http.HandlerFunc,
) {
	registry.Register(httpx.Route{
		ID:      id,
		Method:  method,
		Pattern: pattern,
		Handler: handler,
	})
}

// handleHealthz is the plain-text liveness endpoint.
func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleV1Health is the JSON liveness endpoint.
func handleV1Health(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, healthDTO{OK: true})
}

// handleV1Status returns the deployment status.
func handleV1Status(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status, err := svc.Status(r.Context())
		if err != nil {
			httpx.WriteJSON(w, http.StatusServiceUnavailable, statusDTO{
				Nodes:   []nodeHealthDTO{},
				Healthy: false,
			})
			return
		}
		httpx.WriteJSON(w, http.StatusOK, toStatusDTO(status))
	}
}
