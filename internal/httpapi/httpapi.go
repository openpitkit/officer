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
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"go.openpit.dev/officer/internal/auth"
	"go.openpit.dev/officer/internal/backend"
	"go.openpit.dev/officer/internal/backup"
	"go.openpit.dev/officer/internal/businesscsv"
	"go.openpit.dev/officer/internal/domain"
	"go.openpit.dev/officer/internal/engine"
	"go.openpit.dev/officer/internal/node"
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

// Service is the control-plane seam the HTTP surface calls into. It is
// satisfied by *backend.Service.
type Service interface {
	Status(ctx context.Context) (backend.Status, error)
	ListAccounts(ctx context.Context) ([]domain.Account, error)
	ExportBackup(ctx context.Context, scope backup.Scope) (backup.Archive, string, error)
	RestoreBackup(
		ctx context.Context,
		archive backup.Archive,
		opts backup.RestoreOptions,
	) (backup.RestoreSummary, error)
	ExportBusinessCSV(
		ctx context.Context,
		req backend.BusinessCSVExportRequest,
	) (businesscsv.ExportFile, error)
	PreviewBusinessCSVImport(
		ctx context.Context,
		req backend.BusinessCSVImportRequest,
	) (backend.BusinessCSVImportPreview, error)
	ImportBusinessCSV(
		ctx context.Context,
		req backend.BusinessCSVImportRequest,
	) (backend.BusinessCSVImportResult, error)
	ResetDatabase(ctx context.Context) error
	CreateAccount(ctx context.Context, id domain.AccountID) (domain.Account, error)
	GetAccountState(ctx context.Context, id domain.AccountID) (domain.Account, node.AccountLimits, error)
	BlockAccount(ctx context.Context, id domain.AccountID, reason string) error
	UnblockAccount(ctx context.Context, id domain.AccountID) error
	DeleteAccount(ctx context.Context, id domain.AccountID, force bool) error
	SetAccountGroup(ctx context.Context, id domain.AccountID, groupCode string) error
	SetAccountNotes(ctx context.Context, id domain.AccountID, notes string) error
	ListLimits(ctx context.Context, account domain.AccountID) (node.AccountLimits, error)
	PutRateLimit(ctx context.Context, limit domain.LimitRate) error
	PutOrderSizeLimit(ctx context.Context, limit domain.LimitOrderSize) error
	PutPnlBoundsLimit(ctx context.Context, limit domain.LimitPnlBounds) error
	DeleteLimit(ctx context.Context, target node.LimitTarget) error
	ListAudit(ctx context.Context, count int) ([]domain.AuditRow, error)
	ListAuditFiltered(
		ctx context.Context, filter domain.AuditFilter, count int,
	) ([]domain.AuditRow, error)

	ListMcpAccess(ctx context.Context) ([]backend.McpCommand, error)
	SetMcpAccess(ctx context.Context, command string, enabled bool) error

	WelcomeSeen(ctx context.Context) (bool, error)
	SetWelcomeSeen(ctx context.Context, seen bool) error

	ListMarketData(ctx context.Context) (backend.MarketDataStatus, error)
	RestartMarketData(ctx context.Context) error
	VerifyMarketDataSymbol(
		ctx context.Context, id, externalSymbol string,
	) (backend.MarketDataSymbolVerification, error)
	SearchMarketDataSymbols(
		ctx context.Context, id string, input backend.MarketDataSymbolSearchInput,
	) (backend.MarketDataSymbolSearch, error)
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
	ListGroups(ctx context.Context) ([]domain.AccountGroup, error)
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
	ListAdjustments(
		ctx context.Context, account domain.AccountID, source domain.Source, n int,
	) ([]domain.AccountAdjustmentRecord, error)
	ListAllAdjustments(
		ctx context.Context, account domain.AccountID, source domain.Source, n int,
	) ([]domain.AccountAdjustmentRecord, error)

	SubmitOrder(ctx context.Context, o domain.Order) (domain.Order, error)
	CheckOrder(ctx context.Context, probe domain.OrderProbe) (domain.CheckResult, error)
	ApplyExecutionReport(
		ctx context.Context, in domain.ExecutionReportInput,
	) (engine.ExecutionReportResult, error)
	GetOrder(ctx context.Context, id string) (domain.OrderDetail, error)
	ListOrders(
		ctx context.Context, account domain.AccountID, source domain.Source, n int,
	) ([]domain.Order, error)
	ListTrades(
		ctx context.Context, account domain.AccountID, source domain.Source, n int,
	) ([]domain.Trade, error)

	Overview(ctx context.Context, since time.Time) (backend.Overview, error)
	ServiceInfo(ctx context.Context) (backend.ServiceInfo, error)

	// Signing key management.
	GenerateSigningKey(ctx context.Context) (domain.SigningKey, error)
	ImportSigningKey(ctx context.Context, material, format string) (domain.SigningKey, error)
	ListSigningKeys(ctx context.Context) ([]domain.SigningKey, error)
	ActivePublicKey(format string) (string, error)
	GetNoESign(ctx context.Context) (bool, error)
	SetNoESign(ctx context.Context, off bool) error

	// Approval token flow.
	SubmitOrderToken(ctx context.Context, o domain.Order, mode string) (backend.ApprovalToken, error)
	ConfirmExecution(
		ctx context.Context, orderID string, token string, force bool,
	) (domain.Order, error)
	CancelOrder(
		ctx context.Context, orderID string, token, reason string, force bool,
	) (domain.Order, error)
}

// LogSource is the read seam over the in-memory log tail. It is satisfied by
// *logtail.Buffer. Snapshot returns the buffered lines oldest to newest.
type LogSource interface {
	Snapshot() []string
}

// Options configures the router built by NewRouter.
type Options struct {
	// Service is the control-plane service backing /api/v1. Required.
	Service Service
	// SPA is the embedded dashboard filesystem, rooted at the dist directory so
	// "index.html" resolves at its top level. Required.
	SPA fs.FS
	// MCP is the streamable-HTTP MCP handler mounted under /mcp. When nil the
	// /mcp route is not registered; the dashboard and API are unaffected.
	MCP http.Handler
	// Logs is the in-memory log tail backing the service log routes. When nil the
	// /service/logs routes are not registered; the rest of the surface is
	// unaffected.
	Logs LogSource
}

// NewRouter builds the serve-mode HTTP handler. It wires the liveness probe,
// the REST control-plane API, the optional MCP handler, and the embedded SPA.
// It returns an error when a required option is missing.
func NewRouter(opts Options) (http.Handler, error) {
	if opts.Service == nil {
		return nil, errors.New("httpapi: nil service")
	}
	if opts.SPA == nil {
		return nil, errors.New("httpapi: nil SPA filesystem")
	}

	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.Recoverer)

	// Plain-text liveness probe for the container healthcheck.
	router.Get("/healthz", handleHealthz)

	// The same v1 surface is mounted twice so the panel and the programmatic API
	// are distinguishable server-side without a spoofable header: per-mount
	// middleware stamps the source into the request context, and the backend
	// reads the caller from there. /api/v1 is the public API (documented next
	// phase); /app/api/v1 is the operator-panel mirror (the SPA switches its base
	// path to it in a later phase). Per-mount authentication attaches here later.
	router.Route("/api/v1", func(v1 chi.Router) {
		v1.Use(limitBody)
		v1.Use(stampSource(domain.SourceAPI))
		mountV1(v1, opts.Service, opts.Logs)
	})
	router.Route("/app/api/v1", func(v1 chi.Router) {
		v1.Use(limitBody)
		v1.Use(stampSource(domain.SourcePanel))
		mountV1(v1, opts.Service, opts.Logs)
	})

	if opts.MCP != nil {
		router.Mount("/mcp", opts.MCP)
	}

	// OpenAPI spec and Swagger UI - registered before the SPA NotFound so they
	// are served by these handlers, not the SPA catch-all.
	router.Get("/api/openapi.yaml", serveOpenAPISpec)
	router.Get("/docs", serveSwaggerUI)

	// Explicit routes are registered above; the SPA fallback must come last so it
	// only catches unmatched paths.
	spa, err := newSPAHandler(opts.SPA)
	if err != nil {
		return nil, err
	}
	router.NotFound(spa.ServeHTTP)

	return router, nil
}

// mountV1 registers the v1 control-plane routes on r. It is the single source of
// truth for the v1 surface so both the /api/v1 and /app/api/v1 mounts expose
// exactly the same handlers; the mounting middleware decides the attributed
// source.
func mountV1(r chi.Router, svc Service, logs LogSource) {
	r.Get("/health", handleV1Health)
	r.Get("/status", handleV1Status(svc))
	r.Get("/service", handleServiceInfo(svc))
	// The log tail is optional: only the serve path supplies it, so the routes
	// register only when a source is present.
	if logs != nil {
		r.Get("/service/logs", handleServiceLogs(logs))
		r.Get("/service/logs/download", handleServiceLogsDownload(logs))
	}
	r.Get("/overview", handleOverview(svc))

	r.Post("/backup/export", handleExportBackup(svc))
	r.Post("/backup/restore", handleRestoreBackup(svc))
	r.Post("/business-csv/export", handleExportBusinessCSV(svc))
	r.Post("/business-csv/import/preview", handlePreviewBusinessCSVImport(svc))
	r.Post("/business-csv/import", handleImportBusinessCSV(svc))
	r.Post("/database/reset", handleResetDatabase(svc))

	r.Get("/accounts", handleListAccounts(svc))
	r.Post("/accounts", handleCreateAccount(svc))
	r.Get("/accounts/{code}", handleGetAccount(svc))
	r.Post("/accounts/{code}/block", handleBlockAccount(svc))
	r.Post("/accounts/{code}/unblock", handleUnblockAccount(svc))
	r.Delete("/accounts/{code}", handleDeleteAccount(svc))
	r.Put("/accounts/{code}/group", handleSetAccountGroup(svc))
	r.Put("/accounts/{code}/notes", handleSetAccountNotes(svc))
	r.Get("/accounts/{code}/adjustments", handleListAccountAdjustments(svc))
	r.Post("/accounts/{code}/adjustments", handleApplyAdjustment(svc))

	r.Get("/groups", handleListGroups(svc))
	r.Post("/groups", handleCreateGroup(svc))
	r.Get("/groups/{code}", handleGetGroup(svc))
	r.Put("/groups/{code}/notes", handleSetGroupNotes(svc))
	r.Post("/groups/{code}/block", handleBlockGroup(svc))
	r.Post("/groups/{code}/unblock", handleUnblockGroup(svc))
	r.Delete("/groups/{code}", handleDeleteGroup(svc))

	r.Get("/balances", handleListBalances(svc))
	r.Get("/adjustments", handleListAdjustments(svc))

	r.Post("/orders", handleSubmitOrder(svc))
	r.Post("/orders/check", handleCheckOrder(svc))
	r.Get("/orders", handleListOrders(svc))
	r.Get("/orders/{externalId}", handleGetOrder(svc))
	r.Post("/orders/{externalId}/execution-reports", handleApplyExecutionReport(svc))
	r.Get("/trades", handleListTrades(svc))

	r.Get("/limits", handleListLimits(svc))
	r.Put("/limits/rate", handlePutRateLimit(svc))
	r.Put("/limits/order-size", handlePutOrderSizeLimit(svc))
	r.Put("/limits/pnl-bounds", handlePutPnlBoundsLimit(svc))
	r.Delete("/limits", handleDeleteLimit(svc))

	r.Get("/audit", handleListAudit(svc))
	r.Get("/audit/actions", handleListAuditActions())

	r.Get("/mcp-access", handleListMcpAccess(svc))
	r.Put("/mcp-access/{command}", handleSetMcpAccess(svc))

	r.Get("/user-settings", handleGetUserSettings(svc))
	r.Put("/user-settings", handleSetUserSettings(svc))

	r.Post("/signing/keys/generate", handleGenerateSigningKey(svc))
	r.Post("/signing/keys/import", handleImportSigningKey(svc))
	r.Get("/signing/keys", handleListSigningKeys(svc))
	r.Get("/signing/keys/active/public", handleGetActivePublicKey(svc))
	r.Get("/signing/config", handleGetSigningConfig(svc))
	r.Put("/signing/config", handleSetSigningConfig(svc))
	r.Post("/orders/submit", handleSubmitOrderToken(svc))
	r.Post("/orders/{externalId}/confirm", handleConfirmExecution(svc))
	r.Post("/orders/{externalId}/cancel", handleCancelOrder(svc))

	r.Get("/market-data", handleListMarketData(svc))
	r.Post("/market-data/restart", handleRestartMarketData(svc))
	r.Post("/market-data/instances", handleCreateMarketDataInstance(svc))
	r.Put("/market-data/instances/{id}/enabled", handleSetMarketDataInstanceEnabled(svc))
	r.Put("/market-data/instances/{id}/settings", handleUpdateMarketDataInstanceSettings(svc))
	r.Delete("/market-data/instances/{id}", handleDeleteMarketDataInstance(svc))
	r.Put("/market-data/instances/{id}/instruments", handleUpsertMarketDataInstrument(svc))
	r.Put("/market-data/instances/{id}/instruments/enabled",
		handleSetMarketDataInstrumentEnabled(svc))
	r.Delete("/market-data/instances/{id}/instruments", handleDeleteMarketDataInstrument(svc))
	r.Post("/market-data/instances/{id}/verify-symbol", handleVerifyMarketDataSymbol(svc))
	r.Post("/market-data/instances/{id}/search-symbols", handleSearchMarketDataSymbols(svc))
}

// stampSource is middleware that stamps the given source onto the request
// context's caller so the backend attributes mutations to the surface the
// request arrived on. The source is fixed per mount and never read from a
// request header or body. The principal is the placeholder "operator" until
// authentication lands; this is the authorization seam - the resolved principal
// (and role) will be filled here once per-mount auth is attached.
func stampSource(source domain.Source) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			caller := domain.Caller{Source: source, Principal: "operator"}
			ctx := auth.ContextWithCaller(r.Context(), caller)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// limitBody caps request bodies. Backup restore and business-CSV import have
// larger caps for uploaded files; every other v1 endpoint keeps the ordinary
// small control-plane cap. A body over the cap fails the next Decode, so an
// oversized or unbounded body cannot exhaust memory.
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, requestBodyLimit(r))
		}
		next.ServeHTTP(w, r)
	})
}

func requestBodyLimit(r *http.Request) int64 {
	switch r.URL.Path {
	case "/api/v1/backup/restore", "/app/api/v1/backup/restore":
		return maxBackupRestoreBody
	case "/api/v1/business-csv/import",
		"/app/api/v1/business-csv/import",
		"/api/v1/business-csv/import/preview",
		"/app/api/v1/business-csv/import/preview":
		return maxImportBody
	default:
		return maxRequestBody
	}
}

// handleHealthz is the plain-text liveness endpoint.
func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleV1Health is the JSON liveness endpoint.
func handleV1Health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthDTO{OK: true})
}

// handleV1Status returns the deployment status.
func handleV1Status(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status, err := svc.Status(r.Context())
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, statusDTO{
				Nodes:   []nodeHealthDTO{},
				Healthy: false,
			})
			return
		}
		writeJSON(w, http.StatusOK, toStatusDTO(status))
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
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if !validBackupScope(req.Scope) {
			writeErrMsg(w, http.StatusBadRequest, "validation",
				"backup scope must include all or at least one section")
			return
		}
		archive, filename, err := svc.ExportBackup(r.Context(), req.Scope)
		if err != nil {
			writeErr(w, err)
			return
		}
		if req.Zip {
			payload, zipFilename, err := zipBackupArchive(archive, filename)
			if err != nil {
				writeErr(w, err)
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
		writeJSON(w, http.StatusOK, archive)
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
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
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
			writeErr(w, err)
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
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"preview": preview})
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
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"result": result})
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
		writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
		return backend.BusinessCSVImportRequest{}, false
	}
	if req.PayloadBase64 == "" {
		writeErrMsg(w, http.StatusBadRequest, "validation",
			"payloadBase64 is required")
		return backend.BusinessCSVImportRequest{}, false
	}
	if requirePolicy && req.ConflictPolicy == "" {
		writeErrMsg(w, http.StatusBadRequest, "validation",
			"conflictPolicy is required")
		return backend.BusinessCSVImportRequest{}, false
	}
	payload, err := decodeBusinessCSVPayloadBase64(
		req.PayloadBase64, businesscsv.MaxImportBytes,
	)
	if err != nil {
		if errors.Is(err, domain.ErrTooLarge) {
			writeErr(w, err)
			return backend.BusinessCSVImportRequest{}, false
		}
		writeErrMsg(w, http.StatusBadRequest, "validation",
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
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if req.Mode == "" {
			writeErrMsg(w, http.StatusBadRequest, "validation",
				"restore mode is required")
			return
		}
		if !validRestoreMode(req.Mode) {
			writeErrMsg(w, http.StatusBadRequest, "validation",
				"unknown restore mode")
			return
		}
		if !validBackupScope(req.Scope) {
			writeErrMsg(w, http.StatusBadRequest, "validation",
				"restore scope must include all or at least one section")
			return
		}
		if req.ArchiveFile == "" && len(req.Archive.Manifest.Sections) == 0 {
			writeErrMsg(w, http.StatusBadRequest, "validation",
				"backup archive is required")
			return
		}
		archive := req.Archive
		if req.ArchiveFile != "" {
			parsed, err := parseBackupArchiveFile(req.ArchiveFilename,
				req.ArchiveFile)
			if err != nil {
				writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
				return
			}
			archive = parsed
		}
		summary, err := svc.RestoreBackup(r.Context(), archive,
			backup.RestoreOptions{Scope: req.Scope, Mode: req.Mode})
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"summary": summary})
	}
}

// handleResetDatabase handles POST /api/v1/database/reset.
func handleResetDatabase(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Confirm bool `json:"confirm"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if !req.Confirm {
			writeErrMsg(w, http.StatusBadRequest, "validation",
				"database reset confirmation is required")
			return
		}
		if err := svc.ResetDatabase(r.Context()); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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

// handleListAccounts handles GET /api/v1/accounts.
func handleListAccounts(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		accounts, err := svc.ListAccounts(r.Context())
		if err != nil {
			writeErr(w, err)
			return
		}
		dtos := make([]accountDTO, 0, len(accounts))
		for _, a := range accounts {
			dtos = append(dtos, toAccountDTO(a))
		}
		writeJSON(w, http.StatusOK, map[string]any{"accounts": dtos})
	}
}

// handleCreateAccount handles POST /api/v1/accounts. The body carries the
// account's public code; the engine assigns its internal id, which is never
// exposed.
func handleCreateAccount(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Code string `json:"code"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		id := domain.AccountID(req.Code)
		if err := domain.ValidateAccountID(id); err != nil {
			writeErr(w, err)
			return
		}
		account, err := svc.CreateAccount(r.Context(), id)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"account": toAccountDTO(account)})
	}
}

// handleGetAccount handles GET /api/v1/accounts/{id}.
func handleGetAccount(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathAccountID(r)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		account, limits, err := svc.GetAccountState(r.Context(), id)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"account": toAccountDTO(account),
			"limits":  toAccountLimitsDTO(limits),
		})
	}
}

// handleBlockAccount handles POST /api/v1/accounts/{id}/block.
func handleBlockAccount(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathAccountID(r)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req struct {
			Reason string `json:"reason"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if err := svc.BlockAccount(r.Context(), id, req.Reason); err != nil {
			writeErr(w, err)
			return
		}
		writeAccount(w, svc, r, id)
	}
}

// handleUnblockAccount handles POST /api/v1/accounts/{id}/unblock.
func handleUnblockAccount(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathAccountID(r)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		if err := svc.UnblockAccount(r.Context(), id); err != nil {
			writeErr(w, err)
			return
		}
		writeAccount(w, svc, r, id)
	}
}

// handleDeleteAccount handles DELETE /api/v1/accounts/{id}.
func handleDeleteAccount(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathAccountID(r)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		if err := svc.DeleteAccount(r.Context(), id, forceQuery(r)); err != nil {
			writeErr(w, err)
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
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": toAccountDTO(account)})
}

// handleSetAccountGroup handles PUT /api/v1/accounts/{id}/group. An empty group
// clears membership.
func handleSetAccountGroup(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathAccountID(r)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req struct {
			Group string `json:"group"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if err := svc.SetAccountGroup(r.Context(), id, req.Group); err != nil {
			writeErr(w, err)
			return
		}
		writeAccount(w, svc, r, id)
	}
}

// handleSetAccountNotes handles PUT /api/v1/accounts/{id}/notes.
func handleSetAccountNotes(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathAccountID(r)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req struct {
			Notes string `json:"notes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if err := svc.SetAccountNotes(r.Context(), id, req.Notes); err != nil {
			writeErr(w, err)
			return
		}
		writeAccount(w, svc, r, id)
	}
}

// handleListLimits handles GET /api/v1/limits[?account=]. It returns the three
// typed barrier shapes (rate / order-size / pnl-bounds) per policy. Barriers
// reference accounts by code; no surrogate or engine id is involved.
func handleListLimits(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		account := domain.AccountID(r.URL.Query().Get("account"))
		limits, err := svc.ListLimits(r.Context(), account)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"limits": toAccountLimitsDTO(limits)})
	}
}

// handlePutRateLimit handles PUT /api/v1/limits/rate. The body is the typed
// rate-limit barrier; the backend validates scope/axes and upserts it.
func handlePutRateLimit(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req rateLimitDTO
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
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
			writeErr(w, err)
			return
		}
		persisted, err := persistedRateLimit(r.Context(), svc, limit)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"rateLimit": toRateLimitDTO(persisted)})
	}
}

// handlePutOrderSizeLimit handles PUT /api/v1/limits/order-size. The body is the
// typed order-size barrier; the backend validates scope/axes and upserts it.
func handlePutOrderSizeLimit(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req orderSizeLimitDTO
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
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
			writeErr(w, err)
			return
		}
		persisted, err := persistedOrderSizeLimit(r.Context(), svc, limit)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"orderSizeLimit": toOrderSizeLimitDTO(persisted)})
	}
}

// handlePutPnlBoundsLimit handles PUT /api/v1/limits/pnl-bounds. The body is the
// typed P&L-bounds kill-switch barrier; the backend validates scope/axes and
// upserts it.
func handlePutPnlBoundsLimit(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req pnlBoundsLimitDTO
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
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
			writeErr(w, err)
			return
		}
		persisted, err := persistedPnlBoundsLimit(r.Context(), svc, limit)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"pnlBoundsLimit": toPnlBoundsLimitDTO(persisted)})
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
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleListAudit handles
// GET /api/v1/audit[?account=&source=&actions=&category=&limit=100]. account
// and source narrow the trail. The action filter resolves from an explicit
// ?actions=a,b include-list when present, else from ?category (control |
// trading | all); it defaults to control so the high-volume trading stream
// (order submissions and execution reports) is hidden unless asked for.
func handleListAudit(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, err := limitParam(r, 100, auditCapREST)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		q := r.URL.Query()
		actions, err := auditActionsFromQuery(q)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		filter := domain.AuditFilter{
			Account: domain.AccountID(q.Get("account")),
			Source:  domain.Source(q.Get("source")),
			Actions: actions,
		}
		rows, err := svc.ListAuditFiltered(r.Context(), filter, n)
		if err != nil {
			writeErr(w, err)
			return
		}
		dtos := make([]auditDTO, 0, len(rows))
		for _, row := range rows {
			dtos = append(dtos, toAuditDTO(row))
		}
		writeJSON(w, http.StatusOK, map[string]any{"entries": dtos})
	}
}

// auditActionsFromQuery resolves the audit action include-set from the request.
// An explicit ?actions=a,b list wins (each name validated against the catalogue;
// a non-empty value that resolves to zero valid names is rejected). Otherwise
// ?category selects a group via domain.AuditActionsForCategory; an unknown
// category is rejected with a 400 error. Absent category defaults to control so
// the high-volume trading stream is hidden unless asked for. A nil result means
// no action filter (all actions).
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
	actions, known := domain.AuditActionsForCategory(strings.TrimSpace(q.Get("category")))
	if !known {
		return nil, fmt.Errorf("unknown audit category %q", q.Get("category"))
	}
	return actions, nil
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
		writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
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
			writeErr(w, err)
			return
		}
		dtos := make([]mcpCommandDTO, 0, len(commands))
		for _, c := range commands {
			dtos = append(dtos, toMcpCommandDTO(c))
		}
		writeJSON(w, http.StatusOK, map[string]any{"commands": dtos})
	}
}

// handleSetMcpAccess handles PUT /api/v1/mcp-access/{command}. The body carries
// the new enabled flag. An unknown command maps onto a 404 via the backend's
// domain.ErrNotFound. The confirmation-on-enable for protective commands is a UI
// concern handled elsewhere; the backend just persists.
func handleSetMcpAccess(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		command, err := pathCommand(r)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if err := svc.SetMcpAccess(r.Context(), command, req.Enabled); err != nil {
			writeErr(w, err)
			return
		}
		// Re-read the catalogue so the response reflects the persisted state and
		// the unchanged metadata of the toggled command.
		commands, err := svc.ListMcpAccess(r.Context())
		if err != nil {
			writeErr(w, err)
			return
		}
		for _, c := range commands {
			if c.Command.Name == command {
				writeJSON(w, http.StatusOK, map[string]any{"command": toMcpCommandDTO(c)})
				return
			}
		}
		// The backend validated the command, so it must be present; treat its
		// absence as an internal inconsistency rather than a 404.
		writeErrMsg(w, http.StatusInternalServerError, "internal", "internal error")
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
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, userSettingsDTO{WelcomeSeen: seen})
	}
}

// handleSetUserSettings handles PUT /api/v1/user-settings, persisting the
// operator's UI preferences and echoing the stored state.
func handleSetUserSettings(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req userSettingsDTO
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if err := svc.SetWelcomeSeen(r.Context(), req.WelcomeSeen); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, userSettingsDTO{WelcomeSeen: req.WelcomeSeen})
	}
}

// --- market data ------------------------------------------------------------

func handleListMarketData(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status, err := svc.ListMarketData(r.Context())
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
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
			writeErr(w, err)
			return
		}
		status, err := svc.ListMarketData(r.Context())
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"marketData": toMarketDataDTO(status),
		})
	}
}

func handleCreateMarketDataInstance(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req marketDataCreateInstanceRequestDTO
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
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
		if req.ExternalID != "" {
			id, err := domain.ParseExternalID(req.ExternalID)
			if err != nil {
				writeErr(w, err)
				return
			}
			instance.ExternalID = id
		}
		created, err := svc.CreateMarketDataInstance(r.Context(), instance)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"instance": toMarketDataInstanceDTO(backend.MarketDataInstanceStatus{
				Instance: created,
			}),
		})
	}
}

func handleUpdateMarketDataInstanceSettings(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathID(r)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req marketDataUpdateInstanceSettingsRequestDTO
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if err := svc.UpdateMarketDataInstanceSettings(
			r.Context(), id, req.Label, req.Credentials,
		); err != nil {
			writeErr(w, err)
			return
		}
		status, err := svc.ListMarketData(r.Context())
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"marketData": toMarketDataDTO(status),
		})
	}
}

func handleSetMarketDataInstanceEnabled(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathID(r)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		instanceID, err := domain.ParseExternalID(id)
		if err != nil {
			writeErr(w, err)
			return
		}
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if err := svc.SetMarketDataInstanceEnabled(r.Context(), id, req.Enabled); err != nil {
			writeErr(w, err)
			return
		}
		status, err := svc.ListMarketData(r.Context())
		if err != nil {
			writeErr(w, err)
			return
		}
		for _, instance := range status.Instances {
			if instance.Instance.ExternalID == instanceID {
				writeJSON(w, http.StatusOK, map[string]any{
					"enabled": instance.Instance.Enabled,
				})
				return
			}
		}
		writeErr(w, domain.ErrNotFound)
	}
}

func handleDeleteMarketDataInstance(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathID(r)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		if err := svc.DeleteMarketDataInstance(r.Context(), id, forceQuery(r)); err != nil {
			writeErr(w, err)
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
		id, err := pathID(r)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req marketDataInstrumentDTO
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		instanceID, err := domain.ParseExternalID(id)
		if err != nil {
			writeErr(w, err)
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
			writeErr(w, err)
			return
		}
		status, err := svc.ListMarketData(r.Context())
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"marketData": toMarketDataDTO(status),
		})
	}
}

func handleSetMarketDataInstrumentEnabled(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathID(r)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req struct {
			ExternalSymbol string `json:"externalSymbol"`
			Enabled        bool   `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if err := svc.SetMarketDataInstrumentEnabled(
			r.Context(), id, req.ExternalSymbol, req.Enabled,
		); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"enabled": req.Enabled})
	}
}

func handleDeleteMarketDataInstrument(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathID(r)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		symbol := r.URL.Query().Get("externalSymbol")
		if err := svc.DeleteMarketDataInstrument(r.Context(), id, symbol); err != nil {
			writeErr(w, err)
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
		id, err := pathID(r)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req struct {
			ExternalSymbol string `json:"externalSymbol"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		out, err := svc.VerifyMarketDataSymbol(r.Context(), id, req.ExternalSymbol)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
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
		id, err := pathID(r)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
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
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if strings.TrimSpace(req.Query) == "" {
			writeErrMsg(w, http.StatusBadRequest, "validation", "query is required")
			return
		}
		// The strike is an optional, caller-supplied decimal criterion. Validate it
		// here so a malformed value (e.g. "abc") is a 400 from the boundary, not a
		// false upstream 502 from the connector's deep decimal parse.
		if err := domain.ValidateMarketDataStrike(strings.TrimSpace(req.Strike)); err != nil {
			writeErr(w, err)
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
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"supported": out.Supported,
			"matches":   toMarketDataSymbolMatchDTOs(out.Matches),
		})
	}
}

// --- groups -----------------------------------------------------------------

// handleListGroups handles GET /api/v1/groups.
func handleListGroups(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		groups, err := svc.ListGroups(r.Context())
		if err != nil {
			writeErr(w, err)
			return
		}
		dtos := make([]groupDTO, 0, len(groups))
		for _, g := range groups {
			dtos = append(dtos, toGroupDTO(g))
		}
		writeJSON(w, http.StatusOK, map[string]any{"groups": dtos})
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
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		group := domain.AccountGroup{Code: req.Code, Title: req.Title, Notes: req.Notes}
		if _, err := svc.CreateGroup(r.Context(), group); err != nil {
			writeErr(w, err)
			return
		}
		writeGroup(w, svc, r, req.Code, http.StatusCreated)
	}
}

// handleGetGroup handles GET /api/v1/groups/{code}.
func handleGetGroup(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := pathGroupCode(r)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		group, accounts, err := svc.GetGroup(r.Context(), code)
		if err != nil {
			writeErr(w, err)
			return
		}
		dtos := make([]accountDTO, 0, len(accounts))
		for _, a := range accounts {
			dtos = append(dtos, toAccountDTO(a))
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"group":    toGroupDTO(group),
			"accounts": dtos,
		})
	}
}

// handleSetGroupNotes handles PUT /api/v1/groups/{code}/notes.
func handleSetGroupNotes(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := pathGroupCode(r)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req struct {
			Notes string `json:"notes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if err := svc.SetGroupNotes(r.Context(), code, req.Notes); err != nil {
			writeErr(w, err)
			return
		}
		writeGroup(w, svc, r, code, http.StatusOK)
	}
}

// handleBlockGroup handles POST /api/v1/groups/{code}/block.
func handleBlockGroup(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := pathGroupCode(r)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req struct {
			Reason string `json:"reason"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if err := svc.SetGroupBlocked(r.Context(), code, true, req.Reason); err != nil {
			writeErr(w, err)
			return
		}
		writeGroup(w, svc, r, code, http.StatusOK)
	}
}

// handleUnblockGroup handles POST /api/v1/groups/{code}/unblock.
func handleUnblockGroup(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := pathGroupCode(r)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		if err := svc.SetGroupBlocked(r.Context(), code, false, ""); err != nil {
			writeErr(w, err)
			return
		}
		writeGroup(w, svc, r, code, http.StatusOK)
	}
}

// handleDeleteGroup handles DELETE /api/v1/groups/{code}.
func handleDeleteGroup(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := pathGroupCode(r)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		if err := svc.DeleteGroup(r.Context(), code); err != nil {
			writeErr(w, err)
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
		writeErr(w, err)
		return
	}
	writeJSON(w, status, map[string]any{"group": toGroupDTO(group)})
}

// --- spot funds -------------------------------------------------------------

// handleListBalances handles GET /api/v1/balances[?account=&asset=].
func handleListBalances(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		balances, err := svc.ListBalances(r.Context(),
			domain.AccountID(q.Get("account")), q.Get("asset"))
		if err != nil {
			writeErr(w, err)
			return
		}
		dtos := make([]balanceDTO, 0, len(balances))
		for _, b := range balances {
			dtos = append(dtos, toBalanceDTO(b))
		}
		writeJSON(w, http.StatusOK, map[string]any{"balances": dtos})
	}
}

// handleApplyAdjustment handles POST /api/v1/accounts/{id}/adjustments. A policy
// reject is a successful call: the rejected record is returned in the body.
func handleApplyAdjustment(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathAccountID(r)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req adjustmentRequestDTO
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		// A caller-supplied external id is optional. When present it must be a
		// well-formed wire form (a malformed one is a 400); the backend uses it
		// verbatim and rejects a duplicate with 409. When absent the backend
		// generates one and returns it on the record.
		var externalID domain.ExternalID
		if req.ExternalID != "" {
			externalID, err = domain.ParseExternalID(req.ExternalID)
			if err != nil {
				writeErr(w, err)
				return
			}
		}
		record, err := svc.ApplyAdjustment(
			r.Context(), id, externalID, fromAdjustmentRequestDTO(req))
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"adjustment": toAdjustmentDTO(record)})
	}
}

// handleListAccountAdjustments handles
// GET /api/v1/accounts/{id}/adjustments[?source=&limit=].
func handleListAccountAdjustments(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathAccountID(r)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		n, err := limitParam(r, listDefaultLimit, listCapREST)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		recs, err := svc.ListAdjustments(r.Context(), id,
			domain.Source(r.URL.Query().Get("source")), n)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"adjustments": toAdjustmentDTOs(recs)})
	}
}

// handleListAdjustments handles GET /api/v1/adjustments[?account=&source=&limit=].
func handleListAdjustments(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, err := limitParam(r, listDefaultLimit, listCapREST)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		q := r.URL.Query()
		recs, err := svc.ListAllAdjustments(r.Context(),
			domain.AccountID(q.Get("account")), domain.Source(q.Get("source")), n)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"adjustments": toAdjustmentDTOs(recs)})
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
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
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
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"order": toOrderDTO(out)})
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
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
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
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"check": toCheckResultDTO(out)})
	}
}

// handleListOrders handles GET /api/v1/orders[?account=&source=&limit=].
func handleListOrders(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, err := limitParam(r, listDefaultLimit, listCapREST)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		q := r.URL.Query()
		orders, err := svc.ListOrders(r.Context(),
			domain.AccountID(q.Get("account")), domain.Source(q.Get("source")), n)
		if err != nil {
			writeErr(w, err)
			return
		}
		dtos := make([]orderDTO, 0, len(orders))
		for _, o := range orders {
			dtos = append(dtos, toOrderDTO(o))
		}
		writeJSON(w, http.StatusOK, map[string]any{"orders": dtos})
	}
}

// handleGetOrder handles GET /api/v1/orders/{externalId}. It returns the order,
// its 1:1 approval envelope (omitted when unsigned), its events, and its trades,
// all addressed by opaque external ids.
func handleGetOrder(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathOrderExternalID(r)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		detail, err := svc.GetOrder(r.Context(), id)
		if err != nil {
			writeErr(w, err)
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
		writeJSON(w, http.StatusOK, body)
	}
}

// handleApplyExecutionReport handles
// POST /api/v1/orders/{id}/execution-reports. The fill's instrument, account,
// and side are taken from the parent order; the body carries only the fill.
func handleApplyExecutionReport(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathOrderExternalID(r)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req struct {
			Quantity  string `json:"quantity"`
			Price     string `json:"price"`
			LockPrice string `json:"lockPrice"`
			Force     bool   `json:"force"`
			Final     bool   `json:"final"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		// The parent order supplies the account, instrument, and side; the report
		// body carries only the fill itself.
		detail, err := svc.GetOrder(r.Context(), id)
		if err != nil {
			writeErr(w, err)
			return
		}
		in := domain.ExecutionReportInput{
			BaseAsset:    detail.Order.BaseAsset,
			QuoteAsset:   detail.Order.QuoteAsset,
			FillQuantity: req.Quantity,
			FillPrice:    req.Price,
			LockPrice:    req.LockPrice,
			Account:      detail.Order.Account,
			Side:         detail.Order.Side,
			Order:        detail.Order.ExternalID,
			Force:        req.Force,
			Final:        req.Final,
		}
		result, err := svc.ApplyExecutionReport(r.Context(), in)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"result": toExecutionResultDTO(result)})
	}
}

// handleListTrades handles GET /api/v1/trades[?account=&source=&limit=].
func handleListTrades(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, err := limitParam(r, listDefaultLimit, listCapREST)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		q := r.URL.Query()
		trades, err := svc.ListTrades(r.Context(),
			domain.AccountID(q.Get("account")), domain.Source(q.Get("source")), n)
		if err != nil {
			writeErr(w, err)
			return
		}
		dtos := make([]tradeDTO, 0, len(trades))
		for _, t := range trades {
			dtos = append(dtos, toTradeDTO(t))
		}
		writeJSON(w, http.StatusOK, map[string]any{"trades": dtos})
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
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"key": toSigningKeyDTO(key)})
	}
}

// handleImportSigningKey handles POST /api/v1/signing/keys/import. The body
// carries the raw key material and the format (pem-pkcs8 | openssh | raw-base64).
func handleImportSigningKey(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req signingKeyImportRequestDTO
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if req.Key == "" {
			writeErrMsg(w, http.StatusBadRequest, "signing", "key material is required")
			return
		}
		if req.Format == "" {
			writeErrMsg(w, http.StatusBadRequest, "signing", "format is required")
			return
		}
		key, err := svc.ImportSigningKey(r.Context(), req.Key, req.Format)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"key": toSigningKeyDTO(key)})
	}
}

// handleListSigningKeys handles GET /api/v1/signing/keys. It returns all keys
// including inactive ones (connectors need public keys to verify in-flight
// tokens). Private material is never returned.
func handleListSigningKeys(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		keys, err := svc.ListSigningKeys(r.Context())
		if err != nil {
			writeErr(w, err)
			return
		}
		dtos := make([]signingKeyDTO, 0, len(keys))
		for _, k := range keys {
			dtos = append(dtos, toSigningKeyDTO(k))
		}
		writeJSON(w, http.StatusOK, map[string]any{"keys": dtos})
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
			writeErrMsg(w, http.StatusBadRequest, "signing",
				"format must be pem-pkcs8, openssh, or raw-base64")
			return
		}
		pub, err := svc.ActivePublicKey(format)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, publicKeyDTO{PublicKey: pub})
	}
}

// handleGetSigningConfig handles GET /api/v1/signing/config. It returns the
// current signing configuration (the global eSign-off flag).
func handleGetSigningConfig(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		noESign, err := svc.GetNoESign(r.Context())
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, signingConfigDTO{NoESign: noESign})
	}
}

// handleSetSigningConfig handles PUT /api/v1/signing/config. The body carries
// the new eSign-off state.
func handleSetSigningConfig(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req signingConfigDTO
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if err := svc.SetNoESign(r.Context(), req.NoESign); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, signingConfigDTO{NoESign: req.NoESign})
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
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		mode := req.Mode
		if mode == "" {
			mode = "immediate"
		}
		if mode != "hold" && mode != "immediate" {
			writeErrMsg(w, http.StatusBadRequest, "signing", "mode must be hold or immediate")
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
		if req.ExternalID != "" {
			id, err := domain.ParseExternalID(req.ExternalID)
			if err != nil {
				writeErr(w, err)
				return
			}
			order.ExternalID = id
		}
		tok, err := svc.SubmitOrderToken(r.Context(), order, mode)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, approvalTokenDTO{
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
		orderID, err := pathOrderExternalID(r)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req confirmExecutionRequestDTO
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if req.Token == "" {
			writeErrMsg(w, http.StatusBadRequest, "signing", "token is required")
			return
		}
		order, err := svc.ConfirmExecution(
			r.Context(), orderID, req.Token, req.Force)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"order": toOrderDTO(order)})
	}
}

// handleCancelOrder handles POST /api/v1/orders/{id}/cancel. The body carries
// the approval token and an optional reason; the handler verifies the token and
// rolls back the held reservation.
func handleCancelOrder(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orderID, err := pathOrderExternalID(r)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req cancelOrderRequestDTO
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if req.Token == "" {
			writeErrMsg(w, http.StatusBadRequest, "signing", "token is required")
			return
		}
		order, err := svc.CancelOrder(
			r.Context(), orderID, req.Token, req.Reason, req.Force)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"order": toOrderDTO(order)})
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
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toOverviewDTO(overview))
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
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toServiceDTO(info))
	}
}

// handleServiceLogs handles GET /api/v1/service/logs. It returns the buffered
// log tail as JSON, oldest line first, alongside the line count.
func handleServiceLogs(logs LogSource) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		lines := logs.Snapshot()
		writeJSON(w, http.StatusOK, serviceLogsDTO{Lines: lines, Count: len(lines)})
	}
}

// handleServiceLogsDownload handles GET /api/v1/service/logs/download. It serves
// the full buffer as a plain-text attachment, lines joined by newlines.
func handleServiceLogsDownload(logs LogSource) http.HandlerFunc {
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

// --- request helpers --------------------------------------------------------

// limitParam reads the ?limit= query parameter, defaulting to def and capping at
// capN. A non-positive or non-integer value is an error.
func limitParam(r *http.Request, def, capN int) (int, error) {
	n := def
	if s := r.URL.Query().Get("limit"); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil || v <= 0 {
			return 0, errors.New("limit must be a positive integer")
		}
		n = v
	}
	if n > capN {
		n = capN
	}
	return n, nil
}

// pathID reads the {id} chi path parameter and URL-decodes it. It is used by the
// market-data routes, whose {id} is the instance's opaque external id (a string
// the backend parses), never a surrogate or engine id.
func pathID(r *http.Request) (string, error) {
	raw := chi.URLParam(r, "id")
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return "", errors.New("invalid URL encoding in id")
	}
	return decoded, nil
}

// pathGroupCode reads the {code} chi path parameter and URL-decodes it. A group
// is addressed by its public code.
func pathGroupCode(r *http.Request) (string, error) {
	raw := chi.URLParam(r, "code")
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return "", errors.New("invalid URL encoding in code")
	}
	return decoded, nil
}

// pathCommand reads the {command} chi path parameter and URL-decodes it. It is
// the MCP-access counterpart to pathID.
func pathCommand(r *http.Request) (string, error) {
	raw := chi.URLParam(r, "command")
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return "", errors.New("invalid URL encoding in command")
	}
	return decoded, nil
}

// pathOrderExternalID reads the {externalId} chi path parameter and URL-decodes
// it. An order is addressed by its opaque external id, never a surrogate id; the
// backend validates the string against the external-id codec.
func pathOrderExternalID(r *http.Request) (string, error) {
	raw := chi.URLParam(r, "externalId")
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return "", errors.New("invalid URL encoding in order external id")
	}
	return decoded, nil
}

// pathAccountID reads the {code} chi path parameter and URL-decodes it. An
// account is addressed by its public code.
func pathAccountID(r *http.Request) (domain.AccountID, error) {
	raw := chi.URLParam(r, "code")
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return "", errors.New("invalid URL encoding in account code")
	}
	return domain.AccountID(decoded), nil
}

// writeErr maps a domain sentinel error to the appropriate HTTP status and
// JSON error body. The codes are: ErrTooLarge -> 413 too_large,
// ErrInvalid -> 400 validation, ErrNotFound -> 404 not_found,
// ErrAlreadyExists/ErrConflict -> 409 conflict,
// ErrHasDependents -> 409 has_dependents,
// ErrTerminalOrder -> 409 terminal_order,
// ErrEngineRestarting -> 503 engine_restarting, ErrNotImplemented -> 501
// not_implemented (the message is surfaced so the operator sees which SDK
// capability is missing), and anything else -> 500 internal. The specific
// sentinels take precedence over the generic 500 path.
func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrTooLarge):
		writeErrMsg(w, http.StatusRequestEntityTooLarge, "too_large", err.Error())
	case errors.Is(err, domain.ErrInvalid):
		writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
	case errors.Is(err, domain.ErrNotFound):
		writeErrMsg(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, domain.ErrAlreadyExists):
		writeErrMsg(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, domain.ErrHasDependents):
		writeHasDependentsErr(w, err)
	case errors.Is(err, domain.ErrTerminalOrder):
		writeErrMsg(w, http.StatusConflict, "terminal_order", domain.ErrTerminalOrder.Error())
	case errors.Is(err, domain.ErrConflict):
		writeErrMsg(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, domain.ErrEngineRestarting):
		writeErrMsg(w, http.StatusServiceUnavailable, "engine_restarting", err.Error())
	case errors.Is(err, domain.ErrNotImplemented):
		writeErrMsg(w, http.StatusNotImplemented, "not_implemented", err.Error())
	case errors.Is(err, domain.ErrUpstream):
		// An external provider failed (e.g. a market-data 403/timeout). This is
		// an expected operational condition, so it is logged at WARN with the
		// detail for operators and answered with a plain, actionable message
		// rather than a scary 500 "unhandled internal error".
		slog.Warn("upstream provider request failed", "error", err)
		writeErrMsg(w, http.StatusBadGateway, "upstream",
			"The market-data provider couldn't complete the request. "+
				"Check the symbol and your provider access, then try again.")
	default:
		slog.Error("unhandled internal error serving request", "error", err)
		writeErrMsg(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

func writeHasDependentsErr(w http.ResponseWriter, err error) {
	var typed domain.HasDependentsError
	if !errors.As(err, &typed) {
		typed = domain.HasDependentsError{}
	}
	dependents := make([]map[string]any, 0, len(typed.Dependents))
	for _, dep := range typed.Dependents {
		dependents = append(dependents, map[string]any{
			"kind":  dep.Kind,
			"count": dep.Count,
		})
	}
	writeJSON(w, http.StatusConflict, map[string]any{
		"error": map[string]any{
			"code":       "has_dependents",
			"message":    domain.ErrHasDependents.Error(),
			"dependents": dependents,
		},
	})
}

// writeErrMsg writes a JSON error body with the given HTTP status, code and
// message.
func writeErrMsg(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}

// writeJSON encodes v as JSON with the given status code.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
