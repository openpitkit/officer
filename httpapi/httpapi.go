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

// Package httpapi is Pit Officer's HTTP surface for the `serve` run mode. It
// serves, behind one chi router, a liveness probe (/healthz), the REST
// control-plane API (/api/v1/*), the streamable-HTTP MCP handler (/mcp), and
// the embedded single-page operator dashboard with an SPA fallback for
// client-side routes.
//
// The wire shape of /api/v1 is the dashboard's contract. Domain types carry no
// JSON tags; this package owns the explicit camelCase response DTOs and the
// mapping from domain types onto them.
package httpapi

import (
	"net/http"

	frameworkapp "go.openpit.dev/officer/framework/app"
	"go.openpit.dev/officer/framework/backend"
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

// RouteConfig returns the Officer HTTP surface over svc: the v1 route registry
// (with the service log routes when logs is non-nil), the per-path body limits,
// and the non-v1 mounts. sealReports answers when the credential store was
// sealed; nil panics. The config carries no AccessMode: the composer, which
// owns the access state, sets it.
func RouteConfig(
	svc backend.ControlPlane,
	logs httpx.LogSource,
	sealReports SealReportSource,
) frameworkapp.RouteConfig {
	return frameworkapp.RouteConfig{
		Routes:      NewRouteRegistry(svc, logs, sealReports),
		BodyLimit:   BodyLimitPolicy(),
		ExtraMounts: ExtraMounts(),
	}
}

// RegisterRoutes registers the application's v1 route table into registry.
// sealReports answers when the credential store was sealed; nil panics, as a
// route table that cannot answer a registered route is a wiring defect.
func RegisterRoutes(
	registry *httpx.RouteRegistry,
	svc backend.ControlPlane,
	logs httpx.LogSource,
	sealReports SealReportSource,
) {
	if sealReports == nil {
		panic("httpapi: RegisterRoutes needs a seal report source")
	}
	register(registry, "health.get", http.MethodGet, "/health", handleV1Health)
	register(registry, "status.get", http.MethodGet, "/status", handleV1Status(svc))
	register(registry, "service.get", http.MethodGet, "/service", handleServiceInfo(svc))
	registerDuringSetup(registry, "credential-storage.get", http.MethodGet, "/credential-storage", handleGetCredentialStorage(svc, sealReports))
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

	registerDuringSetup(registry, "accounts.list.get", http.MethodGet, "/accounts", handleListAccounts(svc))
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

	registerDuringSetup(registry, "signing.keys.generate.post", http.MethodPost, "/signing/keys/generate", handleGenerateSigningKey(svc))
	registerDuringSetup(registry, "signing.keys.import.post", http.MethodPost, "/signing/keys/import", handleImportSigningKey(svc))
	registerDuringSetup(registry, "signing.keys.list.get", http.MethodGet, "/signing/keys", handleListSigningKeys(svc))
	registerDuringSetup(registry, "signing.keys.active-public.get", http.MethodGet, "/signing/keys/active/public", handleGetActivePublicKey(svc))
	registerDuringSetup(registry, "signing.keys.public.get", http.MethodGet, "/signing/keys/{keyId}/public", handleGetSigningKeyPublic(svc))
	registerDuringSetup(registry, "signing.config.get", http.MethodGet, "/signing/config", handleGetSigningConfig(svc))
	registerDuringSetup(registry, "signing.config.put", http.MethodPut, "/signing/config", handleSetSigningConfig(svc))
	register(registry, "orders.submit.post", http.MethodPost, "/orders/submit", handleSubmitOrder(svc))
	register(registry, "orders.send-paper.post", http.MethodPost, "/orders/send-paper", handleSendPaperOrder(svc))
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

	register(registry, "trading.list.get", http.MethodGet, "/trading", handleGetTrading(svc))
	register(registry, "trading.connections.create.post", http.MethodPost, "/trading/connections", handleCreateTradingConnection(svc))
	register(registry, "trading.connections.settings.put", http.MethodPut, "/trading/connections/{id}/settings", handleUpdateTradingConnectionSettings(svc))
	register(registry, "trading.connections.enabled.put", http.MethodPut, "/trading/connections/{id}/enabled", handleSetTradingConnectionEnabled(svc))
	register(registry, "trading.connections.check.post", http.MethodPost, "/trading/connections/{id}/check", handleCheckTradingConnection(svc))
	register(registry, "trading.instruments.upsert.put", http.MethodPut, "/trading/connections/{id}/instruments", handleUpsertTradingInstrument(svc))
	register(registry, "trading.instruments.delete", http.MethodDelete, "/trading/connections/{id}/instruments", handleDeleteTradingInstrument(svc))
	register(registry, "trading.verify-symbol.post", http.MethodPost, "/trading/connections/{id}/verify-symbol", handleVerifyTradingSymbol(svc))
	register(registry, "trading.search-symbols.post", http.MethodPost, "/trading/connections/{id}/search-symbols", handleSearchTradingSymbols(svc))
	register(registry, "trading.access.put", http.MethodPut, "/trading/connections/{id}/access", handlePutTradingAccess(svc))
	register(registry, "trading.access.delete", http.MethodDelete, "/trading/connections/{id}/access", handleRemoveTradingAccess(svc))
	register(registry, "trading.fee-account.put", http.MethodPut, "/trading/connections/{id}/fee-account", handleSetTradingFeeAccount(svc))
	register(registry, "trading.fee-account.delete", http.MethodDelete, "/trading/connections/{id}/fee-account", handleDeleteTradingFeeAccount(svc))
	register(registry, "accounts.trading-destinations.list.get", http.MethodGet, "/accounts/{code}/trading-destinations", handleListTradingDestinations(svc))
}

// NewRouteRegistry builds the application's v1 route registry. sealReports
// answers when the credential store was sealed; nil panics.
func NewRouteRegistry(
	svc backend.ControlPlane,
	logs httpx.LogSource,
	sealReports SealReportSource,
) *httpx.RouteRegistry {
	registry := &httpx.RouteRegistry{}
	RegisterRoutes(registry, svc, logs, sealReports)
	return registry
}

// BodyLimitPolicy returns the application's per-path request body cap policy.
func BodyLimitPolicy() func(*http.Request) int64 {
	return httpx.BodyLimitPolicy(maxRequestBody, map[string]int64{
		"/api/v1/backup/restore":     maxBackupRestoreBody,
		"/app/api/v1/backup/restore": maxBackupRestoreBody,
	})
}

// ExtraMounts returns the application's non-v1 HTTP routes.
func ExtraMounts() []httpx.ExtraMount {
	return []httpx.ExtraMount{
		{
			Method:  http.MethodGet,
			Pattern: "/healthz",
			Handler: http.HandlerFunc(handleHealthz),
			Access:  httpx.AccessPublic,
		},
		{
			Method:  http.MethodGet,
			Pattern: "/api/openapi.yaml",
			Handler: serveEmbeddedFile("application/yaml; charset=utf-8", openapiSpec),
			Access:  httpx.AccessPublic,
		},
		{
			Method:  http.MethodGet,
			Pattern: "/docs",
			Handler: http.HandlerFunc(serveSwaggerUI),
			Access:  httpx.AccessPublic,
		},
		{
			Method:  http.MethodGet,
			Pattern: "/docs/swagger-ui.css",
			Handler: serveEmbeddedFile("text/css; charset=utf-8", swaggerUICSS),
			Access:  httpx.AccessPublic,
		},
		{
			Method:  http.MethodGet,
			Pattern: "/docs/swagger-ui-bundle.js",
			Handler: serveEmbeddedFile("text/javascript; charset=utf-8", swaggerUIBundle),
			Access:  httpx.AccessPublic,
		},
		{
			Method:  http.MethodGet,
			Pattern: "/docs/swagger-initializer.js",
			Handler: serveEmbeddedFile("text/javascript; charset=utf-8", swaggerUIInitializer),
			Access:  httpx.AccessPublic,
		},
		// The notices of the vendored code travel with it; the bundle's first
		// line points at its LICENSE.txt beside it.
		{
			Method:  http.MethodGet,
			Pattern: "/docs/swagger-ui-bundle.js.LICENSE.txt",
			Handler: serveEmbeddedFile("text/plain; charset=utf-8", swaggerUIBundleLicense),
			Access:  httpx.AccessPublic,
		},
		{
			Method:  http.MethodGet,
			Pattern: "/docs/LICENSE",
			Handler: serveEmbeddedFile("text/plain; charset=utf-8", swaggerUILicense),
			Access:  httpx.AccessPublic,
		},
		{
			Method:  http.MethodGet,
			Pattern: "/docs/NOTICE",
			Handler: serveEmbeddedFile("text/plain; charset=utf-8", swaggerUINotice),
			Access:  httpx.AccessPublic,
		},
	}
}

// ServiceLifecycleRoutes returns the process lifecycle routes with handlers
// supplied by the host application.
func ServiceLifecycleRoutes(restart, stop http.Handler) []httpx.Route {
	return []httpx.Route{
		{
			ID:          "service.restart.post",
			Method:      http.MethodPost,
			Pattern:     "/service/restart",
			Handler:     restart,
			DuringSetup: true,
		},
		{
			ID:      "service.stop.post",
			Method:  http.MethodPost,
			Pattern: "/service/stop",
			Handler: stop,
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

// registerDuringSetup registers like register a route whose panel mirror
// answers before first-run setup completes.
func registerDuringSetup(
	registry *httpx.RouteRegistry,
	id string,
	method string,
	pattern string,
	handler http.HandlerFunc,
) {
	registry.Register(httpx.Route{
		ID:          id,
		Method:      method,
		Pattern:     pattern,
		Handler:     handler,
		DuringSetup: true,
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

// handleV1Status returns the service status.
func handleV1Status(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status, err := svc.Status(r.Context())
		if err != nil {
			httpx.WriteJSON(w, http.StatusServiceUnavailable, statusDTO{Healthy: false})
			return
		}
		httpx.WriteJSON(w, http.StatusOK, toStatusDTO(status))
	}
}
