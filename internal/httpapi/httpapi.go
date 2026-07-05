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
	"archive/zip"
	"bytes"
	"compress/flate"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/businesscsv"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
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

// maxImportBody admits a base64-encoded MaxImportBytes payload plus a small
// JSON envelope so the handler can return the domain 413 instead of the
// transport cap's generic decode failure.
const maxImportBody = 192 << 20

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
	register(registry, "business-csv.import-preview.post", http.MethodPost, "/business-csv/import/preview", handlePreviewBusinessCSVImport(svc))
	register(registry, "business-csv.import.post", http.MethodPost, "/business-csv/import", handleImportBusinessCSV(svc))
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
	register(registry, "accounts.notes.put", http.MethodPut, "/accounts/{code}/notes", handleSetAccountNotes(svc))
	register(registry, "accounts.adjustments.list.get", http.MethodGet, "/accounts/{code}/adjustments", handleListAccountAdjustments(svc))
	register(registry, "accounts.adjustments.apply.post", http.MethodPost, "/accounts/{code}/adjustments", handleApplyAdjustment(svc))

	register(registry, "groups.list.get", http.MethodGet, "/groups", handleListGroups(svc))
	register(registry, "groups.create.post", http.MethodPost, "/groups", handleCreateGroup(svc))
	register(registry, "groups.get", http.MethodGet, "/groups/{code}", handleGetGroup(svc))
	register(registry, "groups.update.put", http.MethodPut, "/groups/{code}", handleUpdateGroup(svc))
	register(registry, "groups.notes.put", http.MethodPut, "/groups/{code}/notes", handleSetGroupNotes(svc))
	register(registry, "groups.block.post", http.MethodPost, "/groups/{code}/block", handleBlockGroup(svc))
	register(registry, "groups.unblock.post", http.MethodPost, "/groups/{code}/unblock", handleUnblockGroup(svc))
	register(registry, "groups.delete", http.MethodDelete, "/groups/{code}", handleDeleteGroup(svc))

	register(registry, "balances.list.get", http.MethodGet, "/balances", handleListBalances(svc))
	register(registry, "adjustments.list.get", http.MethodGet, "/adjustments", handleListAdjustments(svc))

	register(registry, "orders.submit.post", http.MethodPost, "/orders", handleSubmitOrder(svc))
	register(registry, "orders.check.post", http.MethodPost, "/orders/check", handleCheckOrder(svc))
	register(registry, "orders.list.get", http.MethodGet, "/orders", handleListOrders(svc))
	register(registry, "orders.get", http.MethodGet, "/orders/{externalId}", handleGetOrder(svc))
	register(registry, "orders.execution-reports.post", http.MethodPost, "/orders/{externalId}/execution-reports", handleApplyExecutionReport(svc))
	register(registry, "trades.list.get", http.MethodGet, "/trades", handleListTrades(svc))

	register(registry, "limits.list.get", http.MethodGet, "/limits", handleListLimits(svc))
	register(registry, "limits.rate.put", http.MethodPut, "/limits/rate", handlePutRateLimit(svc))
	register(registry, "limits.order-size.put", http.MethodPut, "/limits/order-size", handlePutOrderSizeLimit(svc))
	register(registry, "limits.pnl-bounds.put", http.MethodPut, "/limits/pnl-bounds", handlePutPnlBoundsLimit(svc))
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
	register(registry, "signing.config.get", http.MethodGet, "/signing/config", handleGetSigningConfig(svc))
	register(registry, "signing.config.put", http.MethodPut, "/signing/config", handleSetSigningConfig(svc))
	register(registry, "orders.submit-token.post", http.MethodPost, "/orders/submit", handleSubmitOrderToken(svc))
	register(registry, "orders.confirm.post", http.MethodPost, "/orders/{externalId}/confirm", handleConfirmExecution(svc))
	register(registry, "orders.cancel.post", http.MethodPost, "/orders/{externalId}/cancel", handleCancelOrder(svc))

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
		"/api/v1/backup/restore":                  maxBackupRestoreBody,
		"/app/api/v1/backup/restore":              maxBackupRestoreBody,
		"/api/v1/business-csv/import":             maxImportBody,
		"/app/api/v1/business-csv/import":         maxImportBody,
		"/api/v1/business-csv/import/preview":     maxImportBody,
		"/app/api/v1/business-csv/import/preview": maxImportBody,
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

// handleExportBackup handles POST /api/v1/backup/export.
func handleExportBackup(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Scope backup.Scope `json:"scope"`
			Zip   bool         `json:"zip"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if !validBackupScope(req.Scope) {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation",
				"backup scope must include all or at least one section")
			return
		}
		archive, filename, err := svc.ExportBackup(r.Context(), req.Scope)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		if req.Zip {
			payload, zipFilename, err := zipBackupArchive(archive, filename)
			if err != nil {
				httpx.WriteErr(w, err)
				return
			}
			w.Header().Set("Content-Disposition",
				`attachment; filename="`+zipFilename+`"`)
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write(payload)
			return
		}
		w.Header().Set("Content-Disposition",
			`attachment; filename="`+filename+`"`)
		httpx.WriteJSON(w, http.StatusOK, archive)
	}
}

// handleExportBusinessCSV handles POST /api/v1/business-csv/export.
func handleExportBusinessCSV(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Entity    string `json:"entity"`
			Delimiter string `json:"delimiter"`
			Filters   struct {
				GroupCode *string `json:"groupCode"`
				Account   string  `json:"account"`
				Asset     string  `json:"asset"`
				Source    string  `json:"source"`
			} `json:"filters"`
			Zip bool `json:"zip"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		file, err := svc.ExportBusinessCSV(r.Context(), backend.BusinessCSVExportRequest{
			Entity:    businesscsv.Entity(req.Entity),
			Delimiter: businesscsv.Delimiter(req.Delimiter),
			Zip:       req.Zip,
			Filter: businesscsv.ExportFilter{
				GroupCode:    businessCSVGroupCode(req.Filters.GroupCode),
				Account:      domain.AccountID(req.Filters.Account),
				Asset:        req.Filters.Asset,
				Source:       domain.Source(req.Filters.Source),
				GroupCodeSet: req.Filters.GroupCode != nil,
			},
		})
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		w.Header().Set("Content-Disposition",
			`attachment; filename="`+file.Name+`"`)
		w.Header().Set("Content-Type", file.ContentType)
		_, _ = w.Write(file.Body)
	}
}

func businessCSVGroupCode(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// handlePreviewBusinessCSVImport handles
// POST /api/v1/business-csv/import/preview.
func handlePreviewBusinessCSVImport(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := readBusinessCSVImportRequest(w, r, false)
		if !ok {
			return
		}
		preview, err := svc.PreviewBusinessCSVImport(r.Context(), req)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"preview": preview})
	}
}

// handleImportBusinessCSV handles POST /api/v1/business-csv/import.
// Imports are atomic: any row error rolls back the whole import. The stop
// policy deliberately commits rows applied before the first conflict and returns
// 200 with counts.stopped=true and conflicts; 409 is reserved for concurrent
// create races.
func handleImportBusinessCSV(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := readBusinessCSVImportRequest(w, r, true)
		if !ok {
			return
		}
		result, err := svc.ImportBusinessCSV(r.Context(), req)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"result": result})
	}
}

func readBusinessCSVImportRequest(
	w http.ResponseWriter, r *http.Request, requirePolicy bool,
) (backend.BusinessCSVImportRequest, bool) {
	var req struct {
		Entity         string `json:"entity"`
		Delimiter      string `json:"delimiter"`
		Filename       string `json:"filename"`
		PayloadBase64  string `json:"payloadBase64"`
		ConflictPolicy string `json:"conflictPolicy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
		return backend.BusinessCSVImportRequest{}, false
	}
	if req.PayloadBase64 == "" {
		httpx.WriteErrMsg(w, http.StatusBadRequest, "validation",
			"payloadBase64 is required")
		return backend.BusinessCSVImportRequest{}, false
	}
	if requirePolicy && req.ConflictPolicy == "" {
		httpx.WriteErrMsg(w, http.StatusBadRequest, "validation",
			"conflictPolicy is required")
		return backend.BusinessCSVImportRequest{}, false
	}
	payload, err := decodeBusinessCSVPayloadBase64(
		req.PayloadBase64, businesscsv.MaxImportBytes,
	)
	if err != nil {
		if errors.Is(err, domain.ErrTooLarge) {
			httpx.WriteErr(w, err)
			return backend.BusinessCSVImportRequest{}, false
		}
		httpx.WriteErrMsg(w, http.StatusBadRequest, "validation",
			"invalid business CSV file encoding")
		return backend.BusinessCSVImportRequest{}, false
	}
	return backend.BusinessCSVImportRequest{
		Entity:         businesscsv.Entity(req.Entity),
		Delimiter:      businesscsv.Delimiter(req.Delimiter),
		Filename:       req.Filename,
		Payload:        payload,
		ConflictPolicy: businesscsv.ConflictPolicy(req.ConflictPolicy),
	}, true
}

func decodeBusinessCSVPayloadBase64(encoded string, maxBytes int) ([]byte, error) {
	if len(encoded) > base64.StdEncoding.EncodedLen(maxBytes) {
		return nil, businesscsv.NewTooLargeError(false)
	}
	payload, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	if len(payload) > maxBytes {
		return nil, businesscsv.NewTooLargeError(false)
	}
	return payload, nil
}

// handleRestoreBackup handles POST /api/v1/backup/restore.
func handleRestoreBackup(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Archive         backup.Archive     `json:"archive"`
			ArchiveFile     string             `json:"archiveFile"`
			ArchiveFilename string             `json:"archiveFilename"`
			Scope           backup.Scope       `json:"scope"`
			Mode            backup.RestoreMode `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if req.Mode == "" {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation",
				"restore mode is required")
			return
		}
		if !validRestoreMode(req.Mode) {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation",
				"unknown restore mode")
			return
		}
		if !validBackupScope(req.Scope) {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation",
				"restore scope must include all or at least one section")
			return
		}
		if req.ArchiveFile == "" && len(req.Archive.Manifest.Sections) == 0 {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation",
				"backup archive is required")
			return
		}
		archive := req.Archive
		if req.ArchiveFile != "" {
			parsed, err := parseBackupArchiveFile(req.ArchiveFilename,
				req.ArchiveFile)
			if err != nil {
				httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
				return
			}
			archive = parsed
		}
		summary, err := svc.RestoreBackup(r.Context(), archive,
			backup.RestoreOptions{Scope: req.Scope, Mode: req.Mode})
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"summary": summary})
	}
}

// handleResetDatabase handles POST /api/v1/database/reset.
func handleResetDatabase(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Confirm bool `json:"confirm"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if !req.Confirm {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation",
				"database reset confirmation is required")
			return
		}
		if err := svc.ResetDatabase(r.Context()); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func zipBackupArchive(
	archive backup.Archive,
	jsonFilename string,
) ([]byte, string, error) {
	body, err := json.Marshal(archive)
	if err != nil {
		return nil, "", fmt.Errorf("backup export marshal: %w", err)
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	zw.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(w, flate.BestCompression)
	})
	fw, err := zw.CreateHeader(&zip.FileHeader{
		Name:   jsonFilename,
		Method: zip.Deflate,
	})
	if err != nil {
		_ = zw.Close()
		return nil, "", fmt.Errorf("backup export zip entry: %w", err)
	}
	if _, err := fw.Write(body); err != nil {
		_ = zw.Close()
		return nil, "", fmt.Errorf("backup export zip write: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, "", fmt.Errorf("backup export zip close: %w", err)
	}
	return out.Bytes(), backupZipFilename(jsonFilename), nil
}

func backupZipFilename(jsonFilename string) string {
	if filepath.Ext(jsonFilename) == ".json" {
		return strings.TrimSuffix(jsonFilename, ".json") + ".zip"
	}
	return jsonFilename + ".zip"
}

func parseBackupArchiveFile(
	filename string,
	encoded string,
) (backup.Archive, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return backup.Archive{}, fmt.Errorf("invalid backup file encoding")
	}
	if isZipPayload(raw) {
		raw, err = readBackupJSONFromZip(raw)
		if err != nil {
			return backup.Archive{}, err
		}
	}
	var archive backup.Archive
	if err := json.Unmarshal(raw, &archive); err != nil {
		if filename == "" {
			return backup.Archive{}, fmt.Errorf("invalid backup archive JSON")
		}
		return backup.Archive{},
			fmt.Errorf("invalid backup archive JSON in %s", filename)
	}
	return archive, nil
}

func isZipPayload(raw []byte) bool {
	return len(raw) >= 4 &&
		raw[0] == 'P' &&
		raw[1] == 'K' &&
		raw[2] == 0x03 &&
		raw[3] == 0x04
}

func readBackupJSONFromZip(raw []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("invalid backup zip archive")
	}
	var candidate *zip.File
	for _, file := range zr.File {
		if file.FileInfo().IsDir() || filepath.Ext(file.Name) != ".json" {
			continue
		}
		if candidate != nil {
			return nil, fmt.Errorf("backup zip contains multiple JSON files")
		}
		candidate = file
	}
	if candidate == nil {
		return nil, fmt.Errorf("backup zip contains no JSON archive")
	}
	rc, err := candidate.Open()
	if err != nil {
		return nil, fmt.Errorf("open backup JSON from zip: %w", err)
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(io.LimitReader(rc, maxBackupRestoreBody+1))
	if err != nil {
		return nil, fmt.Errorf("read backup JSON from zip: %w", err)
	}
	if int64(len(body)) > maxBackupRestoreBody {
		return nil, fmt.Errorf("backup JSON in zip exceeds size limit")
	}
	return body, nil
}

func validBackupScope(scope backup.Scope) bool {
	scope = scope.Normalize()
	if scope.All {
		return true
	}
	if len(scope.Sections) == 0 {
		return false
	}
	for _, section := range scope.Sections {
		if !slices.Contains(backup.AllSections, section) {
			return false
		}
	}
	return true
}

func validRestoreMode(mode backup.RestoreMode) bool {
	switch mode {
	case backup.RestoreModeReplaceAll,
		backup.RestoreModeOverwrite,
		backup.RestoreModeInsertMissing:
		return true
	default:
		return false
	}
}

func textMatcherFromQuery(q url.Values, valueKey, modeKey string) (
	store.TextMatcher, error,
) {
	value := q.Get(valueKey)
	if value == "" {
		return store.TextMatcher{}, nil
	}
	mode := q.Get(modeKey)
	matcher := store.TextMatcher{Fragments: strings.Split(value, "*")}
	switch mode {
	case "", "contains":
	case "starts_with":
		matcher.AnchorStart = true
	case "ends_with":
		matcher.AnchorEnd = true
	case "exact":
		matcher.AnchorStart = true
		matcher.AnchorEnd = true
	default:
		return store.TextMatcher{}, fmt.Errorf("invalid %s", modeKey)
	}
	return matcher, nil
}

func externalIDFromQuery(q url.Values) (domain.ExternalID, error) {
	value := q.Get("id")
	if value == "" {
		value = q.Get("externalId")
	}
	if value == "" {
		return domain.ExternalID{}, nil
	}
	return domain.ParseExternalID(value)
}

func statusFilterFromQuery(q url.Values) (store.StatusFilter, error) {
	switch q.Get("status") {
	case "", "all":
		return store.StatusFilterAll, nil
	case "active":
		return store.StatusFilterActive, nil
	case "blocked":
		return store.StatusFilterBlocked, nil
	default:
		return store.StatusFilterAll, fmt.Errorf("invalid status")
	}
}

// accountCountRangeFromQuery parses the group account-count range filter. It
// honours the legacy has/none/all selector (accountCount=...) and the
// from/to/between range form (accountCountMode/Min/Max).
func accountCountRangeFromQuery(q url.Values) (store.CountRangeFilter, error) {
	if legacy := q.Get("accountCount"); legacy != "" {
		switch legacy {
		case "all":
			return store.CountRangeFilter{}, nil
		case "has":
			zero := 0
			return store.CountRangeFilter{Min: &zero, MinExclusive: true}, nil
		case "none":
			zero := 0
			return store.CountRangeFilter{Min: &zero, Max: &zero}, nil
		default:
			return store.CountRangeFilter{}, fmt.Errorf("invalid accountCount")
		}
	}
	return countRangeFromQuery(
		q, "accountCountMode", "accountCountMin", "accountCountMax",
	)
}

func countRangeFilterFromQuery(q url.Values) (store.CountRangeFilter, error) {
	if legacy := q.Get("positionCount"); legacy != "" {
		switch legacy {
		case "all":
			return store.CountRangeFilter{}, nil
		case "has":
			zero := 0
			return store.CountRangeFilter{Min: &zero, MinExclusive: true}, nil
		case "none":
			zero := 0
			return store.CountRangeFilter{Min: &zero, Max: &zero}, nil
		default:
			return store.CountRangeFilter{}, fmt.Errorf("invalid positionCount")
		}
	}
	return countRangeFromQuery(q, "positionCountMode", "positionCountMin", "positionCountMax")
}

func countRangeFromQuery(
	q url.Values, modeKey string, minKey string, maxKey string,
) (store.CountRangeFilter, error) {
	switch q.Get(modeKey) {
	case "", "all":
		return store.CountRangeFilter{}, nil
	case "eq", "equal", "equals", "exact":
		value, err := nonNegativeIntFromQuery(q, minKey)
		if err != nil {
			return store.CountRangeFilter{}, err
		}
		return store.CountRangeFilter{Equal: &value}, nil
	case "neq", "not_equal", "not_equals":
		value, err := nonNegativeIntFromQuery(q, minKey)
		if err != nil {
			return store.CountRangeFilter{}, err
		}
		return store.CountRangeFilter{NotEqual: &value}, nil
	case "gt", "greater_than":
		value, err := nonNegativeIntFromQuery(q, minKey)
		if err != nil {
			return store.CountRangeFilter{}, err
		}
		return store.CountRangeFilter{Min: &value, MinExclusive: true}, nil
	case "gte":
		value, err := nonNegativeIntFromQuery(q, minKey)
		if err != nil {
			return store.CountRangeFilter{}, err
		}
		return store.CountRangeFilter{Min: &value}, nil
	case "lt", "less_than":
		value, err := nonNegativeIntFromQuery(q, maxKey)
		if err != nil {
			return store.CountRangeFilter{}, err
		}
		return store.CountRangeFilter{Max: &value, MaxExclusive: true}, nil
	case "lte":
		value, err := nonNegativeIntFromQuery(q, maxKey)
		if err != nil {
			return store.CountRangeFilter{}, err
		}
		return store.CountRangeFilter{Max: &value}, nil
	case "between":
		minValue, err := nonNegativeIntFromQuery(q, minKey)
		if err != nil {
			return store.CountRangeFilter{}, err
		}
		maxValue, err := nonNegativeIntFromQuery(q, maxKey)
		if err != nil {
			return store.CountRangeFilter{}, err
		}
		if minValue > maxValue {
			return store.CountRangeFilter{}, fmt.Errorf("invalid %s range", modeKey)
		}
		return store.CountRangeFilter{Min: &minValue, Max: &maxValue}, nil
	default:
		return store.CountRangeFilter{}, fmt.Errorf("invalid %s", modeKey)
	}
}

func nonNegativeIntFromQuery(q url.Values, key string) (int, error) {
	raw := q.Get(key)
	if raw == "" {
		return 0, fmt.Errorf("missing %s", key)
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("invalid %s", key)
	}
	return value, nil
}

func pageSpecFromQuery(q url.Values) (store.PageSpec, error) {
	limit := listDefaultLimit
	if raw := q.Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			return store.PageSpec{}, fmt.Errorf("invalid limit")
		}
		if value > listCapREST {
			value = listCapREST
		}
		limit = value
	}
	offset := 0
	if raw := q.Get("offset"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			return store.PageSpec{}, fmt.Errorf("invalid offset")
		}
		offset = value
	} else if raw := q.Get("page"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			return store.PageSpec{}, fmt.Errorf("invalid page")
		}
		if value-1 > math.MaxInt/limit {
			return store.PageSpec{}, fmt.Errorf("invalid page")
		}
		offset = (value - 1) * limit
	}
	return store.PageSpec{Limit: limit, Offset: offset}, nil
}

func sortSpecFromQuery(q url.Values, allowed map[string]struct{}) (store.SortSpec, error) {
	column := q.Get("sort")
	order := q.Get("order")
	if column == "" {
		if order != "" && order != "asc" && order != "desc" {
			return store.SortSpec{}, fmt.Errorf("invalid order")
		}
		return store.SortSpec{}, nil
	}
	if _, ok := allowed[column]; !ok {
		return store.SortSpec{}, fmt.Errorf("invalid sort")
	}
	switch order {
	case "none":
		return store.SortSpec{}, nil
	case "", "asc":
		return store.SortSpec{Column: column}, nil
	case "desc":
		return store.SortSpec{Column: column, Descending: true}, nil
	default:
		return store.SortSpec{}, fmt.Errorf("invalid order")
	}
}

func decimalRangeFromQuery(
	q url.Values,
	modeKey string,
	minKey string,
	maxKey string,
) (store.DecimalRangeFilter, error) {
	switch q.Get(modeKey) {
	case "", "all":
		return store.DecimalRangeFilter{}, nil
	case "eq", "equal", "equals", "exact":
		value, _, err := decimalBoundFromQuery(q, minKey)
		if err != nil {
			return store.DecimalRangeFilter{}, err
		}
		return store.DecimalRangeFilter{Equal: &value}, nil
	case "neq", "not_equal", "not_equals":
		value, _, err := decimalBoundFromQuery(q, minKey)
		if err != nil {
			return store.DecimalRangeFilter{}, err
		}
		return store.DecimalRangeFilter{NotEqual: &value}, nil
	case "gt", "greater_than":
		value, _, err := decimalBoundFromQuery(q, minKey)
		if err != nil {
			return store.DecimalRangeFilter{}, err
		}
		return store.DecimalRangeFilter{Min: &value, MinExclusive: true}, nil
	case "gte":
		value, _, err := decimalBoundFromQuery(q, minKey)
		if err != nil {
			return store.DecimalRangeFilter{}, err
		}
		return store.DecimalRangeFilter{Min: &value}, nil
	case "lt", "less_than":
		value, _, err := decimalBoundFromQuery(q, maxKey)
		if err != nil {
			return store.DecimalRangeFilter{}, err
		}
		return store.DecimalRangeFilter{Max: &value, MaxExclusive: true}, nil
	case "lte":
		value, _, err := decimalBoundFromQuery(q, maxKey)
		if err != nil {
			return store.DecimalRangeFilter{}, err
		}
		return store.DecimalRangeFilter{Max: &value}, nil
	case "between":
		minValue, minDec, err := decimalBoundFromQuery(q, minKey)
		if err != nil {
			return store.DecimalRangeFilter{}, err
		}
		maxValue, maxDec, err := decimalBoundFromQuery(q, maxKey)
		if err != nil {
			return store.DecimalRangeFilter{}, err
		}
		if minDec.GreaterThan(maxDec) {
			return store.DecimalRangeFilter{}, fmt.Errorf("invalid %s range", modeKey)
		}
		return store.DecimalRangeFilter{Min: &minValue, Max: &maxValue}, nil
	default:
		return store.DecimalRangeFilter{}, fmt.Errorf("invalid %s", modeKey)
	}
}

// decimalBoundFromQuery reads a decimal range bound, validating it is a real
// decimal and returning the plain string for the filter (the store compares it
// numerically through the column's DECIMAL collation) alongside the parsed value
// for an order check.
func decimalBoundFromQuery(
	q url.Values, key string,
) (string, decimal.Decimal, error) {
	raw := q.Get(key)
	if raw == "" {
		return "", decimal.Decimal{}, fmt.Errorf("missing %s", key)
	}
	value, err := decimal.NewFromString(raw)
	if err != nil {
		return "", decimal.Decimal{}, fmt.Errorf("invalid %s", key)
	}
	return raw, value, nil
}

func timeRangeFromQuery(
	q url.Values,
	modeKey string,
	minKey string,
	maxKey string,
) (store.TimeRangeFilter, error) {
	switch q.Get(modeKey) {
	case "", "all":
		return store.TimeRangeFilter{}, nil
	case "gt", "after", "greater_than":
		value, err := timeFromQuery(q, minKey)
		if err != nil {
			return store.TimeRangeFilter{}, err
		}
		return store.TimeRangeFilter{Min: &value, MinExclusive: true}, nil
	case "gte", "on_or_after":
		value, err := timeFromQuery(q, minKey)
		if err != nil {
			return store.TimeRangeFilter{}, err
		}
		return store.TimeRangeFilter{Min: &value}, nil
	case "lt", "before", "less_than":
		value, err := timeFromQuery(q, maxKey)
		if err != nil {
			return store.TimeRangeFilter{}, err
		}
		return store.TimeRangeFilter{Max: &value, MaxExclusive: true}, nil
	case "lte", "on_or_before":
		value, err := timeFromQuery(q, maxKey)
		if err != nil {
			return store.TimeRangeFilter{}, err
		}
		return store.TimeRangeFilter{Max: &value}, nil
	case "between":
		minValue, err := timeFromQuery(q, minKey)
		if err != nil {
			return store.TimeRangeFilter{}, err
		}
		maxValue, err := timeFromQuery(q, maxKey)
		if err != nil {
			return store.TimeRangeFilter{}, err
		}
		if minValue.After(maxValue) {
			return store.TimeRangeFilter{}, fmt.Errorf("invalid %s range", modeKey)
		}
		return store.TimeRangeFilter{Min: &minValue, Max: &maxValue}, nil
	default:
		return store.TimeRangeFilter{}, fmt.Errorf("invalid %s", modeKey)
	}
}

func timeFromQuery(q url.Values, key string) (time.Time, error) {
	raw := q.Get(key)
	if raw == "" {
		return time.Time{}, fmt.Errorf("missing %s", key)
	}
	value, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid %s", key)
	}
	return value.UTC(), nil
}

var accountSortKeys = map[string]struct{}{
	"blockReason":   {},
	"code":          {},
	"group":         {},
	"positionCount": {},
	"status":        {},
	"title":         {},
}

var assetSortKeys = map[string]struct{}{
	"assetClass": {},
	"code":       {},
	"title":      {},
}

var orderSortKeys = map[string]struct{}{
	"account":     {},
	"amountValue": {},
	"at":          {},
	"baseAsset":   {},
	"price":       {},
	"quoteAsset":  {},
	"side":        {},
	"source":      {},
	"status":      {},
}

var groupSortKeys = map[string]struct{}{
	"accountCount":  {},
	"blockReason":   {},
	"code":          {},
	"notes":         {},
	"positionCount": {},
	"status":        {},
	"title":         {},
}

var assetClassSortKeys = map[string]struct{}{
	"assetCount": {},
	"code":       {},
	"title":      {},
}

var policySortKeys = map[string]struct{}{
	"account":     {},
	"asset":       {},
	"initialPnl":  {},
	"lowerBound":  {},
	"maxNotional": {},
	"maxOrders":   {},
	"maxQuantity": {},
	"policy":      {},
	"scope":       {},
	"upperBound":  {},
}

var balanceSortKeys = map[string]struct{}{
	"account":           {},
	"asset":             {},
	"available":         {},
	"averageEntryPrice": {},
	"held":              {},
	"incoming":          {},
	"realizedPnl":       {},
	"updatedAt":         {},
}

var adjustmentSortKeys = map[string]struct{}{
	"account":   {},
	"asset":     {},
	"at":        {},
	"principal": {},
	"source":    {},
	"status":    {},
}

var tradeSortKeys = map[string]struct{}{
	"account":    {},
	"at":         {},
	"baseAsset":  {},
	"lockPrice":  {},
	"price":      {},
	"quantity":   {},
	"quoteAsset": {},
	"side":       {},
	"source":     {},
}

func accountListFilterFromQuery(q url.Values) (store.AccountListFilter, error) {
	code, err := textMatcherFromQuery(q, "code", "codeMatch")
	if err != nil {
		return store.AccountListFilter{}, err
	}
	blockReason, err := textMatcherFromQuery(q, "blockReason", "blockReasonMatch")
	if err != nil {
		return store.AccountListFilter{}, err
	}
	status, err := statusFilterFromQuery(q)
	if err != nil {
		return store.AccountListFilter{}, err
	}
	position, err := countRangeFilterFromQuery(q)
	if err != nil {
		return store.AccountListFilter{}, err
	}
	sortSpec, err := sortSpecFromQuery(q, accountSortKeys)
	if err != nil {
		return store.AccountListFilter{}, err
	}
	page, err := pageSpecFromQuery(q)
	if err != nil {
		return store.AccountListFilter{}, err
	}
	filter := store.AccountListFilter{
		Code:        code,
		BlockReason: blockReason,
		Status:      status,
		Position:    position,
		GroupCode:   nil,
		Sort:        sortSpec,
		Page:        page,
	}
	if values, ok := q["group"]; ok {
		group := ""
		if len(values) > 0 {
			group = values[0]
		}
		filter.GroupCode = &group
	}
	return filter, nil
}

func orderListFilterFromQuery(q url.Values) (store.OrderListFilter, error) {
	baseAsset := store.ExactTextMatcher(q.Get("baseAsset"))
	quoteAsset := store.ExactTextMatcher(q.Get("quoteAsset"))
	side, err := orderSideFromQuery(q)
	if err != nil {
		return store.OrderListFilter{}, err
	}
	status, err := orderStatusFromQuery(q)
	if err != nil {
		return store.OrderListFilter{}, err
	}
	amount, err := decimalRangeFromQuery(q, "amountMode", "amountMin", "amountMax")
	if err != nil {
		return store.OrderListFilter{}, err
	}
	price, err := decimalRangeFromQuery(q, "priceMode", "priceMin", "priceMax")
	if err != nil {
		return store.OrderListFilter{}, err
	}
	at, err := timeRangeFromQuery(q, "atMode", "atMin", "atMax")
	if err != nil {
		return store.OrderListFilter{}, err
	}
	sortSpec, err := sortSpecFromQuery(q, orderSortKeys)
	if err != nil {
		return store.OrderListFilter{}, err
	}
	page, err := pageSpecFromQuery(q)
	if err != nil {
		return store.OrderListFilter{}, err
	}
	return store.OrderListFilter{
		Account:    domain.AccountID(q.Get("account")),
		Source:     domain.Source(q.Get("source")),
		Side:       side,
		Status:     status,
		BaseAsset:  baseAsset,
		QuoteAsset: quoteAsset,
		Amount:     amount,
		Price:      price,
		At:         at,
		Sort:       sortSpec,
		Page:       page,
	}, nil
}

func balanceListFilterFromQuery(q url.Values) (store.BalanceListFilter, error) {
	account := store.ExactTextMatcher(q.Get("account"))
	asset := store.ExactTextMatcher(q.Get("asset"))
	available, err := decimalRangeFromQuery(q, "availableMode", "availableMin", "availableMax")
	if err != nil {
		return store.BalanceListFilter{}, err
	}
	held, err := decimalRangeFromQuery(q, "heldMode", "heldMin", "heldMax")
	if err != nil {
		return store.BalanceListFilter{}, err
	}
	incoming, err := decimalRangeFromQuery(q, "incomingMode", "incomingMin", "incomingMax")
	if err != nil {
		return store.BalanceListFilter{}, err
	}
	averageEntryPrice, err := decimalRangeFromQuery(
		q, "averageEntryPriceMode", "averageEntryPriceMin", "averageEntryPriceMax",
	)
	if err != nil {
		return store.BalanceListFilter{}, err
	}
	realizedPnl, err := decimalRangeFromQuery(
		q, "realizedPnlMode", "realizedPnlMin", "realizedPnlMax",
	)
	if err != nil {
		return store.BalanceListFilter{}, err
	}
	updatedAt, err := timeRangeFromQuery(q, "updatedAtMode", "updatedAfter", "updatedBefore")
	if err != nil {
		return store.BalanceListFilter{}, err
	}
	sortSpec, err := sortSpecFromQuery(q, balanceSortKeys)
	if err != nil {
		return store.BalanceListFilter{}, err
	}
	page, err := pageSpecFromQuery(q)
	if err != nil {
		return store.BalanceListFilter{}, err
	}
	filter := store.BalanceListFilter{
		Account:           account,
		Asset:             asset,
		Available:         available,
		Held:              held,
		Incoming:          incoming,
		AverageEntryPrice: averageEntryPrice,
		RealizedPnl:       realizedPnl,
		UpdatedAt:         updatedAt,
		Sort:              sortSpec,
		Page:              page,
	}
	if values, ok := q["groupCode"]; ok {
		groupCode := ""
		if len(values) > 0 {
			groupCode = values[0]
		}
		filter.GroupCode = &groupCode
	}
	return filter, nil
}

func orderSideFromQuery(q url.Values) (*domain.OrderSide, error) {
	switch raw := q.Get("side"); raw {
	case "", "all":
		return nil, nil
	case string(domain.OrderSideBuy), string(domain.OrderSideSell):
		side := domain.OrderSide(raw)
		return &side, nil
	default:
		return nil, fmt.Errorf("invalid side")
	}
}

func sourceFromQuery(q url.Values) (domain.Source, error) {
	switch raw := q.Get("source"); raw {
	case "", "all":
		return "", nil
	default:
		return domain.Source(raw), nil
	}
}

func adjustmentStatusFromQuery(q url.Values) (*domain.AdjustmentStatus, error) {
	switch raw := q.Get("status"); raw {
	case "", "all":
		return nil, nil
	case string(domain.AdjustmentStatusAccepted), string(domain.AdjustmentStatusRejected):
		status := domain.AdjustmentStatus(raw)
		return &status, nil
	default:
		return nil, fmt.Errorf("invalid status")
	}
}

func orderStatusFromQuery(q url.Values) ([]domain.OrderStatus, error) {
	rawValues := q["status"]
	if len(rawValues) == 0 {
		return nil, nil
	}
	out := make([]domain.OrderStatus, 0, len(rawValues))
	for _, raw := range rawValues {
		for _, part := range strings.Split(raw, ",") {
			if part == "" || part == "all" {
				continue
			}
			status := domain.OrderStatus(part)
			if !validOrderStatus(status) {
				return nil, fmt.Errorf("invalid status")
			}
			out = append(out, status)
		}
	}
	return out, nil
}

func validOrderStatus(status domain.OrderStatus) bool {
	switch status {
	case domain.OrderStatusSubmitted,
		domain.OrderStatusAccepted,
		domain.OrderStatusRejected,
		domain.OrderStatusCommitted,
		domain.OrderStatusRolledBack,
		domain.OrderStatusFilled,
		domain.OrderStatusPartiallyFilled,
		domain.OrderStatusCancelled:
		return true
	default:
		return false
	}
}

func groupListFilterFromQuery(q url.Values) (store.GroupListFilter, error) {
	code, err := textMatcherFromQuery(q, "code", "codeMatch")
	if err != nil {
		return store.GroupListFilter{}, err
	}
	notes, err := textMatcherFromQuery(q, "notes", "notesMatch")
	if err != nil {
		return store.GroupListFilter{}, err
	}
	blockReason, err := textMatcherFromQuery(q, "blockReason", "blockReasonMatch")
	if err != nil {
		return store.GroupListFilter{}, err
	}
	status, err := statusFilterFromQuery(q)
	if err != nil {
		return store.GroupListFilter{}, err
	}
	position, err := countRangeFilterFromQuery(q)
	if err != nil {
		return store.GroupListFilter{}, err
	}
	account, err := accountCountRangeFromQuery(q)
	if err != nil {
		return store.GroupListFilter{}, err
	}
	sortSpec, err := sortSpecFromQuery(q, groupSortKeys)
	if err != nil {
		return store.GroupListFilter{}, err
	}
	page, err := pageSpecFromQuery(q)
	if err != nil {
		return store.GroupListFilter{}, err
	}
	return store.GroupListFilter{
		Code:        code,
		Notes:       notes,
		BlockReason: blockReason,
		Status:      status,
		Position:    position,
		Account:     account,
		Sort:        sortSpec,
		Page:        page,
	}, nil
}

func assetClassListFilterFromQuery(q url.Values) (store.AssetClassListFilter, error) {
	code, err := textMatcherFromQuery(q, "code", "codeMatch")
	if err != nil {
		return store.AssetClassListFilter{}, err
	}
	notes, err := textMatcherFromQuery(q, "notes", "notesMatch")
	if err != nil {
		return store.AssetClassListFilter{}, err
	}
	sortSpec, err := sortSpecFromQuery(q, assetClassSortKeys)
	if err != nil {
		return store.AssetClassListFilter{}, err
	}
	page, err := pageSpecFromQuery(q)
	if err != nil {
		return store.AssetClassListFilter{}, err
	}
	return store.AssetClassListFilter{
		Code:  code,
		Notes: notes,
		Sort:  sortSpec,
		Page:  page,
	}, nil
}

func assetListFilterFromQuery(q url.Values) (store.AssetListFilter, error) {
	code, err := textMatcherFromQuery(q, "code", "codeMatch")
	if err != nil {
		return store.AssetListFilter{}, err
	}
	class, err := textMatcherFromQuery(q, "class", "classMatch")
	if err != nil {
		return store.AssetListFilter{}, err
	}
	sortSpec, err := sortSpecFromQuery(q, assetSortKeys)
	if err != nil {
		return store.AssetListFilter{}, err
	}
	page, err := pageSpecFromQuery(q)
	if err != nil {
		return store.AssetListFilter{}, err
	}
	return store.AssetListFilter{
		Code:  code,
		Class: class,
		Sort:  sortSpec,
		Page:  page,
	}, nil
}

// policyKindFromQuery maps the Limits UI policy selector to the store kind. The
// "all" value (and an absent param) leaves the kind unrestricted; any other
// value is rejected.
func policyKindFromQuery(q url.Values) (*store.PolicyKind, error) {
	switch q.Get("policy") {
	case "", "all":
		return nil, nil
	case "rate":
		kind := store.PolicyKindRate
		return &kind, nil
	case "order_size":
		kind := store.PolicyKindOrderSize
		return &kind, nil
	case "pnl_bounds":
		kind := store.PolicyKindPnlBounds
		return &kind, nil
	default:
		return nil, fmt.Errorf("invalid policy")
	}
}

func policyListFilterFromQuery(q url.Values) (store.PolicyListFilter, error) {
	account := store.ExactTextMatcher(q.Get("account"))
	asset := store.ExactTextMatcher(q.Get("asset"))
	kind, err := policyKindFromQuery(q)
	if err != nil {
		return store.PolicyListFilter{}, err
	}
	sortSpec, err := sortSpecFromQuery(q, policySortKeys)
	if err != nil {
		return store.PolicyListFilter{}, err
	}
	page, err := pageSpecFromQuery(q)
	if err != nil {
		return store.PolicyListFilter{}, err
	}
	return store.PolicyListFilter{
		Account: account,
		Asset:   asset,
		Kind:    kind,
		Sort:    sortSpec,
		Page:    page,
	}, nil
}

func adjustmentListFilterFromQuery(q url.Values) (store.AdjustmentListFilter, error) {
	externalID, err := externalIDFromQuery(q)
	if err != nil {
		return store.AdjustmentListFilter{}, err
	}
	account := store.ExactTextMatcher(q.Get("account"))
	asset := store.ExactTextMatcher(q.Get("asset"))
	source, err := sourceFromQuery(q)
	if err != nil {
		return store.AdjustmentListFilter{}, err
	}
	status, err := adjustmentStatusFromQuery(q)
	if err != nil {
		return store.AdjustmentListFilter{}, err
	}
	at, err := timeRangeFromQuery(q, "atMode", "atMin", "atMax")
	if err != nil {
		return store.AdjustmentListFilter{}, err
	}
	sortSpec, err := sortSpecFromQuery(q, adjustmentSortKeys)
	if err != nil {
		return store.AdjustmentListFilter{}, err
	}
	page, err := pageSpecFromQuery(q)
	if err != nil {
		return store.AdjustmentListFilter{}, err
	}
	return store.AdjustmentListFilter{
		Account:    account,
		Asset:      asset,
		ExternalID: externalID,
		Source:     source,
		Status:     status,
		At:         at,
		Sort:       sortSpec,
		Page:       page,
	}, nil
}

func tradeListFilterFromQuery(q url.Values) (store.TradeListFilter, error) {
	account := store.ExactTextMatcher(q.Get("account"))
	externalID, err := externalIDFromQuery(q)
	if err != nil {
		return store.TradeListFilter{}, err
	}
	baseAsset := store.ExactTextMatcher(q.Get("baseAsset"))
	quoteAsset := store.ExactTextMatcher(q.Get("quoteAsset"))
	side, err := orderSideFromQuery(q)
	if err != nil {
		return store.TradeListFilter{}, err
	}
	source, err := sourceFromQuery(q)
	if err != nil {
		return store.TradeListFilter{}, err
	}
	at, err := timeRangeFromQuery(q, "atMode", "atMin", "atMax")
	if err != nil {
		return store.TradeListFilter{}, err
	}
	quantity, err := decimalRangeFromQuery(q, "quantityMode", "quantityMin", "quantityMax")
	if err != nil {
		return store.TradeListFilter{}, err
	}
	price, err := decimalRangeFromQuery(q, "priceMode", "priceMin", "priceMax")
	if err != nil {
		return store.TradeListFilter{}, err
	}
	lockPrice, err := decimalRangeFromQuery(q, "lockPriceMode", "lockPriceMin", "lockPriceMax")
	if err != nil {
		return store.TradeListFilter{}, err
	}
	sortSpec, err := sortSpecFromQuery(q, tradeSortKeys)
	if err != nil {
		return store.TradeListFilter{}, err
	}
	page, err := pageSpecFromQuery(q)
	if err != nil {
		return store.TradeListFilter{}, err
	}
	return store.TradeListFilter{
		Account:    account,
		ExternalID: externalID,
		BaseAsset:  baseAsset,
		QuoteAsset: quoteAsset,
		Side:       side,
		Source:     source,
		At:         at,
		Quantity:   quantity,
		Price:      price,
		LockPrice:  lockPrice,
		Sort:       sortSpec,
		Page:       page,
	}, nil
}

func auditListFilterFromQuery(q url.Values) (store.AuditListFilter, error) {
	account := store.ExactTextMatcher(q.Get("account"))
	asset := store.ExactTextMatcher(q.Get("asset"))
	externalID, err := externalIDFromQuery(q)
	if err != nil {
		return store.AuditListFilter{}, err
	}
	actor, err := textMatcherFromQuery(q, "actor", "actorMatch")
	if err != nil {
		return store.AuditListFilter{}, err
	}
	source, err := sourceFromQuery(q)
	if err != nil {
		return store.AuditListFilter{}, err
	}
	actions, err := auditActionsFromQuery(q)
	if err != nil {
		return store.AuditListFilter{}, err
	}
	category, err := auditCategoryFromQuery(q, len(actions) > 0)
	if err != nil {
		return store.AuditListFilter{}, err
	}
	at, err := timeRangeFromQuery(q, "atMode", "atMin", "atMax")
	if err != nil {
		return store.AuditListFilter{}, err
	}
	if q.Get("sort") != "" || q.Get("order") != "" {
		return store.AuditListFilter{}, fmt.Errorf("audit sorting is not supported")
	}
	page, err := pageSpecFromQuery(q)
	if err != nil {
		return store.AuditListFilter{}, err
	}
	return store.AuditListFilter{
		Account:    account,
		Asset:      asset,
		ExternalID: externalID,
		Actor:      actor,
		Source:     source,
		Actions:    actions,
		Category:   category,
		At:         at,
		Page:       page,
	}, nil
}

// handleListAssets handles GET /api/v1/assets.
func handleListAssets(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filter, err := assetListFilterFromQuery(r.URL.Query())
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		page, err := svc.ListAssetRows(r.Context(), filter)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		dtos := make([]assetDTO, 0, len(page.Rows))
		for _, asset := range page.Rows {
			dtos = append(dtos, toAssetDTO(asset))
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"assets": dtos,
			"total":  page.Total,
		})
	}
}

// handleCreateAsset handles POST /api/v1/assets. The body carries the asset's
// public code, optional title, and optional classification.
func handleCreateAsset(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Code       string `json:"code"`
			Title      string `json:"title"`
			AssetClass string `json:"assetClass"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		asset := domain.Asset{
			Code:       req.Code,
			Title:      req.Title,
			AssetClass: req.AssetClass,
		}
		created, err := svc.CreateAsset(r.Context(), asset)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, map[string]any{"asset": toAssetDTO(created)})
	}
}

// handleUpdateAsset handles PUT /api/v1/assets/{code}. The path code identifies
// the asset; the body carries the replacement public code, title and
// classification, so an asset can be renamed. Dependent rows reference the asset
// by its surrogate id, mirroring the group rename.
func handleUpdateAsset(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := httpx.PathGroupCode(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req struct {
			Code       string `json:"code"`
			Title      string `json:"title"`
			AssetClass string `json:"assetClass"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		updated, err := svc.UpdateAsset(r.Context(), code, domain.Asset{
			Code:       req.Code,
			Title:      req.Title,
			AssetClass: req.AssetClass,
		})
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"asset": toAssetDTO(updated)})
	}
}

// handleListAssetClasses handles GET /api/v1/asset-classes.
func handleListAssetClasses(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filter, err := assetClassListFilterFromQuery(r.URL.Query())
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		page, err := svc.ListAssetClassRows(r.Context(), filter)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		dtos := make([]assetClassDTO, 0, len(page.Rows))
		for _, row := range page.Rows {
			dtos = append(dtos, toAssetClassRowDTO(row))
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"assetClasses": dtos,
			"total":        page.Total,
		})
	}
}

// handleCreateAssetClass handles POST /api/v1/asset-classes. The body carries
// the class public code, an optional title, and optional notes.
func handleCreateAssetClass(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Code  string `json:"code"`
			Title string `json:"title"`
			Notes string `json:"notes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		created, err := svc.CreateAssetClass(r.Context(), domain.AssetClass{
			Code:  req.Code,
			Title: req.Title,
			Notes: req.Notes,
		})
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, map[string]any{"assetClass": toAssetClassDTO(created)})
	}
}

// handleUpdateAssetClass handles PUT /api/v1/asset-classes/{code}. The path code
// identifies the class; the body carries the replacement public code, title and
// notes, so a class can be renamed. A rename cascades the asset link.
func handleUpdateAssetClass(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := httpx.PathGroupCode(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req struct {
			Code  string `json:"code"`
			Title string `json:"title"`
			Notes string `json:"notes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		updated, err := svc.UpdateAssetClass(r.Context(), code, domain.AssetClass{
			Code:  req.Code,
			Title: req.Title,
			Notes: req.Notes,
		})
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"assetClass": toAssetClassDTO(updated)})
	}
}

// handleDeleteAssetClass handles DELETE /api/v1/asset-classes/{code}.
func handleDeleteAssetClass(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := httpx.PathGroupCode(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		if err := svc.DeleteAssetClass(r.Context(), code, forceQuery(r)); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleDeleteAsset handles DELETE /api/v1/assets/{code}.
func handleDeleteAsset(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := httpx.PathGroupCode(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		if err := svc.DeleteAsset(r.Context(), code, forceQuery(r)); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleListAccounts handles GET /api/v1/accounts.
func handleListAccounts(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filter, err := accountListFilterFromQuery(r.URL.Query())
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		accounts, err := svc.ListAccountRows(r.Context(), filter)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		dtos := make([]accountDTO, 0, len(accounts.Rows))
		for _, row := range accounts.Rows {
			dtos = append(dtos, toAccountRowDTO(row))
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"accounts": dtos,
			"total":    accounts.Total,
		})
	}
}

// handleCreateAccount handles POST /api/v1/accounts. The body carries the
// account's public code and optional title; the engine assigns its internal id,
// which is never exposed.
func handleCreateAccount(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Code  string `json:"code"`
			Title string `json:"title"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		account, err := svc.CreateAccount(r.Context(), domain.Account{
			Code:  domain.AccountID(req.Code),
			Title: req.Title,
		})
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, map[string]any{"account": toAccountDTO(account)})
	}
}

// handleGetAccount handles GET /api/v1/accounts/{id}.
func handleGetAccount(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathAccountID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		account, limits, err := svc.GetAccountState(r.Context(), id)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"account": toAccountDTO(account),
			"limits":  toAccountLimitsDTO(limits),
		})
	}
}

// handleUpdateAccount handles PUT /api/v1/accounts/{id}. The body carries the
// replacement public code and title.
func handleUpdateAccount(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathAccountID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req struct {
			Code  string `json:"code"`
			Title string `json:"title"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		account, err := svc.UpdateAccount(r.Context(), id, domain.Account{
			Code:  domain.AccountID(req.Code),
			Title: req.Title,
		})
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"account": toAccountDTO(account)})
	}
}

// handleBlockAccount handles POST /api/v1/accounts/{id}/block.
func handleBlockAccount(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathAccountID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req struct {
			Reason string `json:"reason"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if err := svc.BlockAccount(r.Context(), id, req.Reason); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		writeAccount(w, svc, r, id)
	}
}

// handleUnblockAccount handles POST /api/v1/accounts/{id}/unblock.
func handleUnblockAccount(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathAccountID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		if err := svc.UnblockAccount(r.Context(), id); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		writeAccount(w, svc, r, id)
	}
}

// handleDeleteAccount handles DELETE /api/v1/accounts/{id}.
func handleDeleteAccount(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathAccountID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		if err := svc.DeleteAccount(r.Context(), id, forceQuery(r)); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// writeAccount re-reads the account and writes it as {"account": {...}} with a
// 200. Account-mutating handlers (block/unblock, group, notes) use it so the
// response body is valid JSON reflecting the real persisted state, not an empty
// 200 the SPA would fail to parse. The limits returned alongside are ignored.
func writeAccount(w http.ResponseWriter, svc Service, r *http.Request, id domain.AccountID) {
	account, _, err := svc.GetAccountState(r.Context(), id)
	if err != nil {
		httpx.WriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"account": toAccountDTO(account)})
}

// handleSetAccountGroup handles PUT /api/v1/accounts/{id}/group. An empty group
// clears membership.
func handleSetAccountGroup(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathAccountID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req struct {
			Group string `json:"group"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if err := svc.SetAccountGroup(r.Context(), id, req.Group); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		writeAccount(w, svc, r, id)
	}
}

// handleSetAccountNotes handles PUT /api/v1/accounts/{id}/notes.
func handleSetAccountNotes(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathAccountID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req struct {
			Notes string `json:"notes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if err := svc.SetAccountNotes(r.Context(), id, req.Notes); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		writeAccount(w, svc, r, id)
	}
}

// handleListLimits handles GET /api/v1/limits. It returns the three typed
// barrier tables flattened into one sorted, paged policy list. The optional
// account, policy (kind), sort/order, and limit/offset query params narrow and
// order the result; barriers reference accounts by code, never a surrogate id.
func handleListLimits(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filter, err := policyListFilterFromQuery(r.URL.Query())
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		page, err := svc.ListPolicyRows(r.Context(), filter)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		dtos := make([]policyDTO, 0, len(page.Rows))
		for _, row := range page.Rows {
			dtos = append(dtos, toPolicyRowDTO(row))
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"policies": dtos,
			"total":    page.Total,
		})
	}
}

// handlePutRateLimit handles PUT /api/v1/limits/rate. The body is the typed
// rate-limit barrier; the backend validates scope/axes and upserts it.
func handlePutRateLimit(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req rateLimitDTO
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		limit := domain.LimitRate{
			Scope:     req.Scope,
			Account:   domain.AccountID(req.Account),
			Asset:     req.Asset,
			Window:    time.Duration(req.WindowMs) * time.Millisecond,
			MaxOrders: req.MaxOrders,
		}
		if err := svc.PutRateLimit(r.Context(), limit); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		persisted, err := persistedRateLimit(r.Context(), svc, limit)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"rateLimit": toRateLimitDTO(persisted)})
	}
}

// handlePutOrderSizeLimit handles PUT /api/v1/limits/order-size. The body is the
// typed order-size barrier; the backend validates scope/axes and upserts it.
func handlePutOrderSizeLimit(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req orderSizeLimitDTO
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		limit := domain.LimitOrderSize{
			Scope:       req.Scope,
			Account:     domain.AccountID(req.Account),
			Asset:       req.Asset,
			MaxQuantity: req.MaxQuantity,
			MaxNotional: req.MaxNotional,
		}
		if err := svc.PutOrderSizeLimit(r.Context(), limit); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		persisted, err := persistedOrderSizeLimit(r.Context(), svc, limit)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"orderSizeLimit": toOrderSizeLimitDTO(persisted)})
	}
}

// handlePutPnlBoundsLimit handles PUT /api/v1/limits/pnl-bounds. The body is the
// typed P&L-bounds kill-switch barrier; the backend validates scope/axes and
// upserts it.
func handlePutPnlBoundsLimit(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req pnlBoundsLimitDTO
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		limit := domain.LimitPnlBounds{
			Scope:      req.Scope,
			Account:    domain.AccountID(req.Account),
			Asset:      req.Asset,
			LowerBound: req.LowerBound,
			UpperBound: req.UpperBound,
			InitialPnl: req.InitialPnl,
		}
		if err := svc.PutPnlBoundsLimit(r.Context(), limit); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		persisted, err := persistedPnlBoundsLimit(r.Context(), svc, limit)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"pnlBoundsLimit": toPnlBoundsLimitDTO(persisted)})
	}
}

func persistedRateLimit(
	ctx context.Context, svc Service, target domain.LimitRate,
) (domain.LimitRate, error) {
	limits, err := svc.ListLimits(ctx, target.Account)
	if err != nil {
		return domain.LimitRate{}, err
	}
	for _, limit := range limits.RateLimits {
		if sameLimitAddress(limit.Scope, limit.Account, limit.Asset,
			target.Scope, target.Account, target.Asset) {
			return limit, nil
		}
	}
	return domain.LimitRate{}, domain.ErrNotFound
}

func persistedOrderSizeLimit(
	ctx context.Context, svc Service, target domain.LimitOrderSize,
) (domain.LimitOrderSize, error) {
	limits, err := svc.ListLimits(ctx, target.Account)
	if err != nil {
		return domain.LimitOrderSize{}, err
	}
	for _, limit := range limits.OrderSizeLimits {
		if sameLimitAddress(limit.Scope, limit.Account, limit.Asset,
			target.Scope, target.Account, target.Asset) {
			return limit, nil
		}
	}
	return domain.LimitOrderSize{}, domain.ErrNotFound
}

func persistedPnlBoundsLimit(
	ctx context.Context, svc Service, target domain.LimitPnlBounds,
) (domain.LimitPnlBounds, error) {
	limits, err := svc.ListLimits(ctx, target.Account)
	if err != nil {
		return domain.LimitPnlBounds{}, err
	}
	for _, limit := range limits.PnlBoundsLimits {
		if sameLimitAddress(limit.Scope, limit.Account, limit.Asset,
			target.Scope, target.Account, target.Asset) {
			return limit, nil
		}
	}
	return domain.LimitPnlBounds{}, domain.ErrNotFound
}

func sameLimitAddress(
	leftScope string, leftAccount domain.AccountID, leftAsset string,
	rightScope string, rightAccount domain.AccountID, rightAsset string,
) bool {
	return leftScope == rightScope && leftAccount == rightAccount && leftAsset == rightAsset
}

// handleDeleteLimit handles
// DELETE /api/v1/limits?policy=&scope=&account=&asset=. The barrier is addressed
// by its (policy, scope, account-code, asset) composite.
func handleDeleteLimit(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		target := node.LimitTarget{
			Policy:  q.Get("policy"),
			Scope:   q.Get("scope"),
			Account: domain.AccountID(q.Get("account")),
			Asset:   q.Get("asset"),
		}
		if err := svc.DeleteLimit(r.Context(), target); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleListAudit handles
// GET /api/v1/audit[?account=&asset=&source=&actions=&category=&limit=100].
// account, asset, and source narrow the trail. An explicit ?actions=a,b include
// list wins; otherwise ?category selects control, trading, or all actions.
func handleListAudit(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		filter, err := auditListFilterFromQuery(q)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		if filter.Page.Limit > auditCapREST {
			filter.Page.Limit = auditCapREST
		}
		page, err := svc.ListAuditRows(r.Context(), filter)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		dtos := make([]auditDTO, 0, len(page.Rows))
		for _, row := range page.Rows {
			dtos = append(dtos, toAuditDTO(row))
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"entries": dtos,
			"total":   page.Total,
		})
	}
}

// auditActionsFromQuery resolves the explicit audit action include-set. Each
// name is validated against the catalogue; a non-empty value that resolves to
// zero valid names is rejected. Category shortcuts are parsed separately so the
// store can use category-specific predicates instead of large action IN lists.
func auditActionsFromQuery(q url.Values) ([]domain.AuditAction, error) {
	if raw := strings.TrimSpace(q.Get("actions")); raw != "" {
		valid := make(map[domain.AuditAction]struct{})
		for _, action := range domain.AllAuditActions() {
			valid[action] = struct{}{}
		}
		var actions []domain.AuditAction
		for _, name := range strings.Split(raw, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			action := domain.AuditAction(name)
			if _, ok := valid[action]; !ok {
				return nil, fmt.Errorf("unknown audit action %q", name)
			}
			actions = append(actions, action)
		}
		if len(actions) == 0 {
			return nil, fmt.Errorf("no valid audit actions in %q", raw)
		}
		return actions, nil
	}
	return nil, nil
}

func auditCategoryFromQuery(
	q url.Values, hasExplicitActions bool,
) (domain.AuditCategory, error) {
	if hasExplicitActions {
		return "", nil
	}
	switch category := strings.TrimSpace(q.Get("category")); category {
	case "":
		return "", nil
	case string(domain.AuditCategoryControl):
		return domain.AuditCategoryControl, nil
	case string(domain.AuditCategoryTrading):
		return domain.AuditCategoryTrading, nil
	case "all":
		return "", nil
	default:
		return "", fmt.Errorf("unknown audit category %q", q.Get("category"))
	}
}

// handleListAuditActions handles GET /api/v1/audit/actions. It returns the
// audit-action catalogue grouped by category, control first, in canonical
// order. It is the single source the web uses to build the audit type filter,
// so the classification lives only in the domain. It reads no service state.
func handleListAuditActions() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		groups := []auditActionGroupDTO{
			{
				Category: string(domain.AuditCategoryControl),
				Actions: auditActionStrings(
					domain.AuditActionsByCategory(domain.AuditCategoryControl)),
			},
			{
				Category: string(domain.AuditCategoryTrading),
				Actions: auditActionStrings(
					domain.AuditActionsByCategory(domain.AuditCategoryTrading)),
			},
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"groups": groups})
	}
}

// --- MCP access control -----------------------------------------------------

// handleListMcpAccess handles GET /api/v1/mcp-access. It returns the full MCP
// command catalogue, each entry carrying its metadata and current effective
// enabled state.
func handleListMcpAccess(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		commands, err := svc.ListMcpAccess(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		dtos := make([]mcpCommandDTO, 0, len(commands))
		for _, c := range commands {
			dtos = append(dtos, toMcpCommandDTO(c))
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"commands": dtos})
	}
}

// handleSetMcpAccess handles PUT /api/v1/mcp-access/{command}. The body carries
// the new enabled flag. An unknown command maps onto a 404 via the backend's
// domain.ErrNotFound. The confirmation-on-enable for protective commands is a UI
// concern handled elsewhere; the backend just persists.
func handleSetMcpAccess(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		command, err := httpx.PathCommand(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if err := svc.SetMcpAccess(r.Context(), command, req.Enabled); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		// Re-read the catalogue so the response reflects the persisted state and
		// the unchanged metadata of the toggled command.
		commands, err := svc.ListMcpAccess(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		for _, c := range commands {
			if c.Command.Name == command {
				httpx.WriteJSON(w, http.StatusOK, map[string]any{"command": toMcpCommandDTO(c)})
				return
			}
		}
		// The backend validated the command, so it must be present; treat its
		// absence as an internal inconsistency rather than a 404.
		httpx.WriteErrMsg(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

// --- user settings ----------------------------------------------------------

type userSettingsDTO struct {
	WelcomeSeen bool `json:"welcomeSeen"`
}

// handleGetUserSettings handles GET /api/v1/user-settings, returning the current
// operator's UI preferences.
func handleGetUserSettings(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		seen, err := svc.WelcomeSeen(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, userSettingsDTO{WelcomeSeen: seen})
	}
}

// handleSetUserSettings handles PUT /api/v1/user-settings, persisting the
// operator's UI preferences and echoing the stored state.
func handleSetUserSettings(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req userSettingsDTO
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if err := svc.SetWelcomeSeen(r.Context(), req.WelcomeSeen); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, userSettingsDTO{WelcomeSeen: req.WelcomeSeen})
	}
}

// --- market data ------------------------------------------------------------

func handleListMarketData(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status, err := svc.ListMarketData(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"marketData": toMarketDataDTO(status),
		})
	}
}

// handleRestartMarketData handles POST /api/v1/market-data/restart. It
// re-applies the market-data configuration by restarting the connector manager,
// then returns the refreshed snapshot in the same envelope as the other
// market-data mutations.
func handleRestartMarketData(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := svc.RestartMarketData(r.Context()); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		status, err := svc.ListMarketData(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"marketData": toMarketDataDTO(status),
		})
	}
}

func handleCreateMarketDataInstance(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req marketDataCreateInstanceRequestDTO
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		instance := domain.MarketDataInstance{
			Provider:    req.Provider,
			Label:       req.Label,
			Credentials: req.Credentials,
			Enabled:     req.Enabled,
		}
		// A caller-supplied external id is optional. When present it must be a
		// well-formed wire form (a malformed one is a 400); the backend uses it
		// verbatim and rejects a duplicate with 409. When absent the backend
		// generates one and returns it on the instance.
		suppliedID := req.ID
		if suppliedID == "" {
			suppliedID = req.ExternalID
		}
		if suppliedID != "" {
			id, err := domain.ParseExternalID(suppliedID)
			if err != nil {
				httpx.WriteErr(w, err)
				return
			}
			instance.ExternalID = id
		}
		created, err := svc.CreateMarketDataInstance(r.Context(), instance)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, map[string]any{
			"instance": toMarketDataInstanceDTO(backend.MarketDataInstanceStatus{
				Instance: created,
			}),
		})
	}
}

func handleUpdateMarketDataInstanceSettings(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req marketDataUpdateInstanceSettingsRequestDTO
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if err := svc.UpdateMarketDataInstanceSettings(
			r.Context(), id, req.Label, req.Credentials,
		); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		status, err := svc.ListMarketData(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"marketData": toMarketDataDTO(status),
		})
	}
}

func handleSetMarketDataInstanceEnabled(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		instanceID, err := domain.ParseExternalID(id)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if err := svc.SetMarketDataInstanceEnabled(r.Context(), id, req.Enabled); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		status, err := svc.ListMarketData(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		for _, instance := range status.Instances {
			if instance.Instance.ExternalID == instanceID {
				httpx.WriteJSON(w, http.StatusOK, map[string]any{
					"enabled": instance.Instance.Enabled,
				})
				return
			}
		}
		httpx.WriteErr(w, domain.ErrNotFound)
	}
}

func handleDeleteMarketDataInstance(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		if err := svc.DeleteMarketDataInstance(r.Context(), id, forceQuery(r)); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func forceQuery(r *http.Request) bool {
	return r.URL.Query().Get("force") == "true"
}

func handleUpsertMarketDataInstrument(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req marketDataInstrumentDTO
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		instanceID, err := domain.ParseExternalID(id)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		instrument := domain.MarketDataInstrument{
			Instance:       instanceID,
			ExternalSymbol: req.ExternalSymbol,
			BaseAsset:      req.BaseAsset,
			QuoteAsset:     req.QuoteAsset,
			ManualPrice:    req.ManualPrice,
			Enabled:        req.Enabled,
		}
		if err := svc.UpsertMarketDataInstrument(r.Context(), instrument); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		status, err := svc.ListMarketData(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"marketData": toMarketDataDTO(status),
		})
	}
}

func handleSetMarketDataInstrumentEnabled(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req struct {
			ExternalSymbol string `json:"externalSymbol"`
			Enabled        bool   `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if err := svc.SetMarketDataInstrumentEnabled(
			r.Context(), id, req.ExternalSymbol, req.Enabled,
		); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"enabled": req.Enabled})
	}
}

func handleDeleteMarketDataInstrument(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		symbol := r.URL.Query().Get("externalSymbol")
		if err := svc.DeleteMarketDataInstrument(r.Context(), id, symbol); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleVerifyMarketDataSymbol handles POST
// /api/v1/market-data/instances/{id}/verify-symbol. It runs a stateless,
// non-mutating check of whether the external symbol exists on the instance's
// provider; live feeds are untouched. A provider that cannot verify symbols is a
// successful call returning supported=false, not an HTTP error.
func handleVerifyMarketDataSymbol(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req struct {
			ExternalSymbol string `json:"externalSymbol"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		out, err := svc.VerifyMarketDataSymbol(r.Context(), id, req.ExternalSymbol)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"verification": toMarketDataSymbolVerificationDTO(out),
		})
	}
}

// handleSearchMarketDataSymbols handles POST
// /api/v1/market-data/instances/{id}/search-symbols. It runs a stateless,
// non-mutating search of the instance's provider catalogue; live feeds are
// untouched. An empty query (after trimming) is a validation error and never
// reaches the service. A provider that cannot search symbols is a successful
// call returning supported=false, not an HTTP error.
func handleSearchMarketDataSymbols(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req struct {
			Query                        string `json:"query"`
			SecType                      string `json:"secType"`
			Exchange                     string `json:"exchange"`
			Currency                     string `json:"currency"`
			LastTradeDateOrContractMonth string `json:"lastTradeDateOrContractMonth"`
			Right                        string `json:"right"`
			Strike                       string `json:"strike"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if strings.TrimSpace(req.Query) == "" {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "query is required")
			return
		}
		// The strike is an optional, caller-supplied decimal criterion. Validate it
		// here so a malformed value (e.g. "abc") is a 400 from the boundary, not a
		// false upstream 502 from the connector's deep decimal parse.
		if err := domain.ValidateMarketDataStrike(strings.TrimSpace(req.Strike)); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		out, err := svc.SearchMarketDataSymbols(r.Context(), id, backend.MarketDataSymbolSearchInput{
			Query:                        req.Query,
			SecType:                      req.SecType,
			Exchange:                     req.Exchange,
			Currency:                     req.Currency,
			LastTradeDateOrContractMonth: req.LastTradeDateOrContractMonth,
			Right:                        req.Right,
			Strike:                       req.Strike,
		})
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"supported": out.Supported,
			"matches":   toMarketDataSymbolMatchDTOs(out.Matches),
		})
	}
}

// --- groups -----------------------------------------------------------------

// handleListGroups handles GET /api/v1/groups.
func handleListGroups(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filter, err := groupListFilterFromQuery(r.URL.Query())
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		groups, err := svc.ListGroupRows(r.Context(), filter)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		dtos := make([]groupDTO, 0, len(groups.Rows))
		for _, row := range groups.Rows {
			dtos = append(dtos, toGroupRowDTO(row))
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"groups": dtos,
			"total":  groups.Total,
		})
	}
}

// handleCreateGroup handles POST /api/v1/groups. The body carries the group's
// public code, an optional title, and optional notes; the engine assigns its
// internal group id, which is never exposed.
func handleCreateGroup(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Code  string `json:"code"`
			Title string `json:"title"`
			Notes string `json:"notes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		group := domain.AccountGroup{Code: req.Code, Title: req.Title, Notes: req.Notes}
		if _, err := svc.CreateGroup(r.Context(), group); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		writeGroup(w, svc, r, req.Code, http.StatusCreated)
	}
}

// handleGetGroup handles GET /api/v1/groups/{code}.
func handleGetGroup(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := httpx.PathGroupCode(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		group, accounts, err := svc.GetGroup(r.Context(), code)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		dtos := make([]accountDTO, 0, len(accounts))
		for _, a := range accounts {
			dtos = append(dtos, toAccountDTO(a))
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"group":    toGroupDTO(group),
			"accounts": dtos,
		})
	}
}

// handleUpdateGroup handles PUT /api/v1/groups/{code}. The body carries the
// replacement public code and title.
func handleUpdateGroup(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := httpx.PathGroupCode(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req struct {
			Code  string `json:"code"`
			Title string `json:"title"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		group, err := svc.UpdateGroup(r.Context(), code, domain.AccountGroup{
			Code:  req.Code,
			Title: req.Title,
		})
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"group": toGroupDTO(group)})
	}
}

// handleSetGroupNotes handles PUT /api/v1/groups/{code}/notes.
func handleSetGroupNotes(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := httpx.PathGroupCode(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req struct {
			Notes string `json:"notes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if err := svc.SetGroupNotes(r.Context(), code, req.Notes); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		writeGroup(w, svc, r, code, http.StatusOK)
	}
}

// handleBlockGroup handles POST /api/v1/groups/{code}/block.
func handleBlockGroup(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := httpx.PathGroupCode(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req struct {
			Reason string `json:"reason"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if err := svc.SetGroupBlocked(r.Context(), code, true, req.Reason); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		writeGroup(w, svc, r, code, http.StatusOK)
	}
}

// handleUnblockGroup handles POST /api/v1/groups/{code}/unblock.
func handleUnblockGroup(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := httpx.PathGroupCode(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		if err := svc.SetGroupBlocked(r.Context(), code, false, ""); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		writeGroup(w, svc, r, code, http.StatusOK)
	}
}

// handleDeleteGroup handles DELETE /api/v1/groups/{code}.
func handleDeleteGroup(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := httpx.PathGroupCode(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		if err := svc.DeleteGroup(r.Context(), code); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// writeGroup re-reads the group and writes it as {"group": {...}} with status,
// so group-mutating handlers return valid JSON reflecting real server state.
// The member accounts returned alongside the group are ignored here.
func writeGroup(w http.ResponseWriter, svc Service, r *http.Request, code string, status int) {
	group, _, err := svc.GetGroup(r.Context(), code)
	if err != nil {
		httpx.WriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, status, map[string]any{"group": toGroupDTO(group)})
}

// --- spot funds -------------------------------------------------------------

// handleListBalances handles GET /api/v1/balances[?account=&asset=].
func handleListBalances(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filter, err := balanceListFilterFromQuery(r.URL.Query())
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		balances, err := svc.ListBalanceRows(r.Context(), filter)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		dtos := make([]balanceDTO, 0, len(balances.Rows))
		for _, b := range balances.Rows {
			dtos = append(dtos, toBalanceDTO(b.Balance))
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"balances": dtos,
			"total":    balances.Total,
		})
	}
}

// handleApplyAdjustment handles POST /api/v1/accounts/{id}/adjustments. A policy
// reject is a successful call: the rejected record is returned in the body.
func handleApplyAdjustment(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathAccountID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req adjustmentRequestDTO
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		// A caller-supplied external id is optional. When present it must be a
		// well-formed wire form (a malformed one is a 400); the backend uses it
		// verbatim and rejects a duplicate with 409. When absent the backend
		// generates one and returns it on the record.
		var externalID domain.ExternalID
		suppliedID := req.ID
		if suppliedID == "" {
			suppliedID = req.ExternalID
		}
		if suppliedID != "" {
			externalID, err = domain.ParseExternalID(suppliedID)
			if err != nil {
				httpx.WriteErr(w, err)
				return
			}
		}
		record, err := svc.ApplyAdjustment(
			r.Context(), id, externalID, fromAdjustmentRequestDTO(req))
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, map[string]any{"adjustment": toAdjustmentDTO(record)})
	}
}

// handleListAccountAdjustments handles
// GET /api/v1/accounts/{id}/adjustments[?source=&limit=].
func handleListAccountAdjustments(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathAccountID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		n, err := httpx.LimitParam(r, listDefaultLimit, listCapREST)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		recs, err := svc.ListAdjustments(r.Context(), id,
			domain.Source(r.URL.Query().Get("source")), n)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"adjustments": toAdjustmentDTOs(recs)})
	}
}

// handleListAdjustments handles GET /api/v1/adjustments.
func handleListAdjustments(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		filter, err := adjustmentListFilterFromQuery(q)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		page, err := svc.ListAdjustmentRows(r.Context(), filter)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"adjustments": toAdjustmentDTOs(page.Rows),
			"total":       page.Total,
		})
	}
}

func toAdjustmentDTOs(recs []domain.AccountAdjustmentRecord) []adjustmentDTO {
	dtos := make([]adjustmentDTO, 0, len(recs))
	for _, rec := range recs {
		dtos = append(dtos, toAdjustmentDTO(rec))
	}
	return dtos
}

// --- trading ----------------------------------------------------------------

// handleSubmitOrder handles POST /api/v1/orders. This is the submit-like-the-API
// path: it runs the engine pre-trade and records every event, rejects included.
// An engine reject is a successful call returning the order in its rejected
// status, not an HTTP error.
func handleSubmitOrder(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Account     string `json:"account"`
			BaseAsset   string `json:"baseAsset"`
			QuoteAsset  string `json:"quoteAsset"`
			Side        string `json:"side"`
			AmountKind  string `json:"amountKind"`
			AmountValue string `json:"amountValue"`
			Price       string `json:"price"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		order := domain.Order{
			Account:     domain.AccountID(req.Account),
			BaseAsset:   req.BaseAsset,
			QuoteAsset:  req.QuoteAsset,
			Side:        domain.OrderSide(req.Side),
			AmountKind:  domain.OrderAmountKind(req.AmountKind),
			AmountValue: req.AmountValue,
			Price:       req.Price,
		}
		out, err := svc.SubmitOrder(r.Context(), order)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, map[string]any{"order": toOrderDTO(out)})
	}
}

// handleCheckOrder handles POST /api/v1/orders/check. It runs the engine
// pre-trade as a non-mutating dry-run and reports whether the order would pass.
// An engine reject is a successful call returning passed=false with the
// reasons, not an HTTP error; nothing is recorded and no state changes.
func handleCheckOrder(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Account     string `json:"account"`
			BaseAsset   string `json:"baseAsset"`
			QuoteAsset  string `json:"quoteAsset"`
			Side        string `json:"side"`
			AmountKind  string `json:"amountKind"`
			AmountValue string `json:"amountValue"`
			Price       string `json:"price"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		probe := domain.OrderProbe{
			Account:     domain.AccountID(req.Account),
			BaseAsset:   req.BaseAsset,
			QuoteAsset:  req.QuoteAsset,
			Side:        domain.OrderSide(req.Side),
			AmountKind:  domain.OrderAmountKind(req.AmountKind),
			AmountValue: req.AmountValue,
			Price:       req.Price,
		}
		out, err := svc.CheckOrder(r.Context(), probe)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"check": toCheckResultDTO(out)})
	}
}

// handleListOrders handles GET /api/v1/orders[?account=&source=&limit=].
func handleListOrders(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filter, err := orderListFilterFromQuery(r.URL.Query())
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		orders, err := svc.ListOrderRows(r.Context(), filter)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		dtos := make([]orderDTO, 0, len(orders.Rows))
		for _, o := range orders.Rows {
			dtos = append(dtos, toOrderDTO(o.Order))
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"orders": dtos,
			"total":  orders.Total,
		})
	}
}

// handleGetOrder handles GET /api/v1/orders/{externalId}. It returns the order,
// its 1:1 approval envelope (omitted when unsigned), its events, and its trades,
// all addressed by opaque external ids.
func handleGetOrder(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathOrderExternalID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		detail, err := svc.GetOrder(r.Context(), id)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		events := make([]orderEventDTO, 0, len(detail.Events))
		for _, e := range detail.Events {
			events = append(events, toOrderEventDTO(e))
		}
		trades := make([]tradeDTO, 0, len(detail.Trades))
		for _, t := range detail.Trades {
			trades = append(trades, toTradeDTO(t))
		}
		body := map[string]any{
			"order":  toOrderDTO(detail.Order),
			"events": events,
			"trades": trades,
		}
		// The approval envelope is omitted entirely when the order is unsigned, so
		// the wire shape distinguishes "no envelope" from a present one.
		if approval := toOrderApprovalDTO(detail.Approval); approval != nil {
			body["approval"] = approval
		}
		httpx.WriteJSON(w, http.StatusOK, body)
	}
}

// handleApplyExecutionReport handles
// POST /api/v1/orders/{id}/execution-reports. The fill's instrument, account,
// and side are taken from the parent order; the body carries only the fill.
func handleApplyExecutionReport(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathOrderExternalID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req struct {
			Quantity       string `json:"quantity"`
			Price          string `json:"price"`
			LeavesQuantity string `json:"leavesQuantity"`
			LockPrice      string `json:"lockPrice"`
			RealizedPnl    string `json:"realizedPnl"`
			Fee            string `json:"fee"`
			Force          bool   `json:"force"`
			Final          bool   `json:"final"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if req.LeavesQuantity == "" {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "leavesQuantity is required")
			return
		}
		// The parent order supplies the account, instrument, and side; the report
		// body carries only the fill itself.
		detail, err := svc.GetOrder(r.Context(), id)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		in := domain.ExecutionReportInput{
			BaseAsset:      detail.Order.BaseAsset,
			QuoteAsset:     detail.Order.QuoteAsset,
			FillQuantity:   req.Quantity,
			FillPrice:      req.Price,
			LeavesQuantity: req.LeavesQuantity,
			LockPrice:      req.LockPrice,
			RealizedPnl:    req.RealizedPnl,
			Fee:            req.Fee,
			Account:        detail.Order.Account,
			Side:           detail.Order.Side,
			Order:          detail.Order.ExternalID,
			Force:          req.Force,
			Final:          req.Final,
		}
		result, err := svc.ApplyExecutionReport(r.Context(), in)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, map[string]any{"result": toExecutionResultDTO(result)})
	}
}

// handleListTrades handles GET /api/v1/trades.
func handleListTrades(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		filter, err := tradeListFilterFromQuery(q)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		page, err := svc.ListTradeRows(r.Context(), filter)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		dtos := make([]tradeDTO, 0, len(page.Rows))
		for _, t := range page.Rows {
			dtos = append(dtos, toTradeDTO(t))
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"trades": dtos,
			"total":  page.Total,
		})
	}
}

// --- signing keys -----------------------------------------------------------

// handleGenerateSigningKey handles POST /api/v1/signing/keys/generate. It
// generates a fresh Ed25519 keypair, makes it the sole active signing key, and
// returns it without private material.
func handleGenerateSigningKey(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, err := svc.GenerateSigningKey(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, map[string]any{"key": toSigningKeyDTO(key)})
	}
}

// handleImportSigningKey handles POST /api/v1/signing/keys/import. The body
// carries the raw key material and the format (pem-pkcs8 | openssh | raw-base64).
func handleImportSigningKey(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req signingKeyImportRequestDTO
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if req.Key == "" {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "signing", "key material is required")
			return
		}
		if req.Format == "" {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "signing", "format is required")
			return
		}
		key, err := svc.ImportSigningKey(r.Context(), req.Key, req.Format)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, map[string]any{"key": toSigningKeyDTO(key)})
	}
}

// handleListSigningKeys handles GET /api/v1/signing/keys. It returns all keys
// including inactive ones (connectors need public keys to verify in-flight
// tokens). Private material is never returned.
func handleListSigningKeys(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		keys, err := svc.ListSigningKeys(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		dtos := make([]signingKeyDTO, 0, len(keys))
		for _, k := range keys {
			dtos = append(dtos, toSigningKeyDTO(k))
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"keys": dtos})
	}
}

// handleGetActivePublicKey handles GET /api/v1/signing/keys/active/public. The
// optional ?format= parameter selects the export format
// (pem-pkcs8 | openssh | raw-base64); the default is pem-pkcs8.
func handleGetActivePublicKey(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		format := r.URL.Query().Get("format")
		if format == "" {
			format = "pem-pkcs8"
		}
		switch format {
		case "pem-pkcs8", "openssh", "raw-base64":
			// valid
		default:
			httpx.WriteErrMsg(w, http.StatusBadRequest, "signing",
				"format must be pem-pkcs8, openssh, or raw-base64")
			return
		}
		pub, err := svc.ActivePublicKey(format)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, publicKeyDTO{PublicKey: pub})
	}
}

// handleGetSigningConfig handles GET /api/v1/signing/config. It returns the
// current signing configuration (the global eSign-off flag).
func handleGetSigningConfig(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		noESign, err := svc.GetNoESign(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, signingConfigDTO{NoESign: noESign})
	}
}

// handleSetSigningConfig handles PUT /api/v1/signing/config. The body carries
// the new eSign-off state.
func handleSetSigningConfig(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req signingConfigDTO
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if err := svc.SetNoESign(r.Context(), req.NoESign); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, signingConfigDTO{NoESign: req.NoESign})
	}
}

// --- approval token ---------------------------------------------------------

// handleSubmitOrderToken handles POST /api/v1/orders/submit. The body carries
// the order fields, an optional caller-supplied external id, and the submit mode
// (hold | immediate). Submit CREATES the order exactly once: it runs the engine
// pre-trade in the given mode and records the order, then issues a signed
// approval token on accept. The returned orderExternalId is the id actually used
// (the supplied one when valid, else a generated one), so confirm/cancel resolve
// the same order. There is no separate persisting create before submit.
func handleSubmitOrderToken(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req submitOrderTokenRequestDTO
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		mode := req.Mode
		if mode == "" {
			mode = "immediate"
		}
		if mode != "hold" && mode != "immediate" {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "signing", "mode must be hold or immediate")
			return
		}
		order := domain.Order{
			Account:     domain.AccountID(req.Account),
			BaseAsset:   req.BaseAsset,
			QuoteAsset:  req.QuoteAsset,
			Side:        domain.OrderSide(req.Side),
			AmountKind:  domain.OrderAmountKind(req.AmountKind),
			AmountValue: req.AmountValue,
			Price:       req.Price,
		}
		// A caller-supplied external id is optional. When present it must be a
		// well-formed wire form (a malformed one is a 400); the backend uses it
		// verbatim and rejects a duplicate with 409. When absent the backend
		// generates one and returns it.
		suppliedID := req.ID
		if suppliedID == "" {
			suppliedID = req.ExternalID
		}
		if suppliedID != "" {
			id, err := domain.ParseExternalID(suppliedID)
			if err != nil {
				httpx.WriteErr(w, err)
				return
			}
			order.ExternalID = id
		}
		tok, err := svc.SubmitOrderToken(r.Context(), order, mode)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, approvalTokenDTO{
			Token:           tok.Token,
			KeyID:           tok.KeyID,
			ExpiresAt:       tok.ExpiresAt.Format(time.RFC3339Nano),
			OrderExternalID: tok.OrderExternalID,
		})
	}
}

// handleConfirmExecution handles POST /api/v1/orders/{id}/confirm. The body
// carries the approval token; the handler verifies it and commits the held
// reservation.
func handleConfirmExecution(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orderID, err := httpx.PathOrderExternalID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req confirmExecutionRequestDTO
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if req.Token == "" {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "signing", "token is required")
			return
		}
		order, err := svc.ConfirmExecution(
			r.Context(), orderID, req.Token, req.Force)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"order": toOrderDTO(order)})
	}
}

// handleCancelOrder handles POST /api/v1/orders/{id}/cancel. The body carries
// the approval token and an optional reason; the handler verifies the token and
// rolls back the held reservation.
func handleCancelOrder(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orderID, err := httpx.PathOrderExternalID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req cancelOrderRequestDTO
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if req.Token == "" {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "signing", "token is required")
			return
		}
		order, err := svc.CancelOrder(
			r.Context(), orderID, req.Token, req.Reason, req.Force)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"order": toOrderDTO(order)})
	}
}

// --- dashboard / service ----------------------------------------------------

// handleOverview handles GET /api/v1/overview. The optional ?since= query
// parameter (RFC3339) sets the "today" boundary for the orders-today tally;
// when absent or unparseable it falls back to the server-local start of day.
func handleOverview(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		since := startOfTodayLocal()
		if s := r.URL.Query().Get("since"); s != "" {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				since = t
			}
		}
		overview, err := svc.Overview(r.Context(), since)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, toOverviewDTO(overview))
	}
}

// startOfTodayLocal returns the server-local start of the current day
// (00:00:00 in the local time zone). It is the default "today" boundary for the
// overview orders tally when the request omits a usable ?since= parameter.
func startOfTodayLocal() time.Time {
	now := time.Now().Local()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
}

// handleServiceInfo handles GET /api/v1/service.
func handleServiceInfo(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		info, err := svc.ServiceInfo(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, toServiceDTO(info))
	}
}

// handleServiceLogs handles GET /api/v1/service/logs. It returns the buffered
// log tail as JSON, oldest line first, alongside the line count.
func handleServiceLogs(logs httpx.LogSource) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		lines := logs.Snapshot()
		httpx.WriteJSON(w, http.StatusOK, serviceLogsDTO{Lines: lines, Count: len(lines)})
	}
}

// handleServiceLogsDownload handles GET /api/v1/service/logs/download. It serves
// the full buffer as a plain-text attachment, lines joined by newlines.
func handleServiceLogsDownload(logs httpx.LogSource) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		lines := logs.Snapshot()
		body := strings.Join(lines, "\n")
		if len(lines) > 0 {
			body += "\n"
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition",
			`attachment; filename="pit-officer.log"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}
}
