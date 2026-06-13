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
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"go.openpit.dev/officer/internal/auth"
	"go.openpit.dev/officer/internal/backend"
	"go.openpit.dev/officer/internal/domain"
	"go.openpit.dev/officer/internal/engine"
)

// auditCapREST is the maximum number of audit rows the REST endpoint returns.
const auditCapREST = 1000

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
	CreateAccount(ctx context.Context, id domain.AccountID) (domain.Account, error)
	GetAccountState(ctx context.Context, id domain.AccountID) (domain.Account, []domain.Limit, error)
	BlockAccount(ctx context.Context, id domain.AccountID, reason string) error
	UnblockAccount(ctx context.Context, id domain.AccountID) error
	SetAccountGroup(ctx context.Context, id domain.AccountID, groupID string) error
	SetAccountNotes(ctx context.Context, id domain.AccountID, notes string) error
	ListLimits(ctx context.Context, account domain.AccountID) ([]domain.Limit, error)
	PutLimit(ctx context.Context, limit domain.Limit) error
	DeleteLimit(ctx context.Context, target domain.LimitTarget) error
	ListAudit(ctx context.Context, count int) ([]domain.AuditRow, error)
	ListAuditFiltered(
		ctx context.Context, account domain.AccountID, source domain.Source, count int,
	) ([]domain.AuditRow, error)

	ListMcpAccess(ctx context.Context) ([]backend.McpCommand, error)
	SetMcpAccess(ctx context.Context, command string, enabled bool) error

	ListMarketData(ctx context.Context) (backend.MarketDataStatus, error)
	CreateMarketDataInstance(ctx context.Context, instance domain.MarketDataInstance) error
	SetMarketDataInstanceEnabled(ctx context.Context, id string, enabled bool) error
	DeleteMarketDataInstance(ctx context.Context, id string) error
	UpsertMarketDataInstrument(ctx context.Context, instrument domain.MarketDataInstrument) error
	SetMarketDataInstrumentEnabled(
		ctx context.Context, instanceID, externalSymbol string, enabled bool,
	) error
	DeleteMarketDataInstrument(ctx context.Context, instanceID, externalSymbol string) error

	CreateGroup(ctx context.Context, group domain.AccountGroup) error
	ListGroups(ctx context.Context) ([]domain.AccountGroup, error)
	GetGroup(ctx context.Context, id string) (domain.AccountGroup, []domain.Account, error)
	SetGroupNotes(ctx context.Context, id, notes string) error
	SetGroupBlocked(ctx context.Context, id string, blocked bool, reason string) error
	DeleteGroup(ctx context.Context, id string) error

	ApplyAdjustment(
		ctx context.Context, account domain.AccountID, req domain.AdjustmentRequest,
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
	GetOrder(ctx context.Context, id int64) (domain.OrderDetail, error)
	ListOrders(
		ctx context.Context, account domain.AccountID, source domain.Source, n int,
	) ([]domain.Order, error)
	ListTrades(
		ctx context.Context, account domain.AccountID, source domain.Source, n int,
	) ([]domain.Trade, error)

	Overview(ctx context.Context, since time.Time) (backend.Overview, error)
	ServiceInfo(ctx context.Context) (backend.ServiceInfo, error)
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
		v1.Use(stampSource(domain.SourceAPI))
		mountV1(v1, opts.Service)
	})
	router.Route("/app/api/v1", func(v1 chi.Router) {
		v1.Use(stampSource(domain.SourcePanel))
		mountV1(v1, opts.Service)
	})

	if opts.MCP != nil {
		router.Mount("/mcp", opts.MCP)
	}

	// OpenAPI spec and Swagger UI — registered before the SPA NotFound so they
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
func mountV1(r chi.Router, svc Service) {
	r.Get("/health", handleV1Health)
	r.Get("/status", handleV1Status(svc))
	r.Get("/service", handleServiceInfo(svc))
	r.Get("/overview", handleOverview(svc))

	r.Get("/accounts", handleListAccounts(svc))
	r.Post("/accounts", handleCreateAccount(svc))
	r.Get("/accounts/{id}", handleGetAccount(svc))
	r.Post("/accounts/{id}/block", handleBlockAccount(svc))
	r.Post("/accounts/{id}/unblock", handleUnblockAccount(svc))
	r.Put("/accounts/{id}/group", handleSetAccountGroup(svc))
	r.Put("/accounts/{id}/notes", handleSetAccountNotes(svc))
	r.Get("/accounts/{id}/adjustments", handleListAccountAdjustments(svc))
	r.Post("/accounts/{id}/adjustments", handleApplyAdjustment(svc))

	r.Get("/groups", handleListGroups(svc))
	r.Post("/groups", handleCreateGroup(svc))
	r.Get("/groups/{id}", handleGetGroup(svc))
	r.Put("/groups/{id}/notes", handleSetGroupNotes(svc))
	r.Post("/groups/{id}/block", handleBlockGroup(svc))
	r.Post("/groups/{id}/unblock", handleUnblockGroup(svc))
	r.Delete("/groups/{id}", handleDeleteGroup(svc))

	r.Get("/balances", handleListBalances(svc))
	r.Get("/adjustments", handleListAdjustments(svc))

	r.Post("/orders", handleSubmitOrder(svc))
	r.Post("/orders/check", handleCheckOrder(svc))
	r.Get("/orders", handleListOrders(svc))
	r.Get("/orders/{id}", handleGetOrder(svc))
	r.Post("/orders/{id}/execution-reports", handleApplyExecutionReport(svc))
	r.Get("/trades", handleListTrades(svc))

	r.Get("/limits", handleListLimits(svc))
	r.Put("/limits", handlePutLimit(svc))
	r.Delete("/limits", handleDeleteLimit(svc))

	r.Get("/audit", handleListAudit(svc))

	r.Get("/mcp-access", handleListMcpAccess(svc))
	r.Put("/mcp-access/{command}", handleSetMcpAccess(svc))

	r.Get("/market-data", handleListMarketData(svc))
	r.Post("/market-data/instances", handleCreateMarketDataInstance(svc))
	r.Put("/market-data/instances/{id}/enabled", handleSetMarketDataInstanceEnabled(svc))
	r.Delete("/market-data/instances/{id}", handleDeleteMarketDataInstance(svc))
	r.Put("/market-data/instances/{id}/instruments", handleUpsertMarketDataInstrument(svc))
	r.Put("/market-data/instances/{id}/instruments/enabled",
		handleSetMarketDataInstrumentEnabled(svc))
	r.Delete("/market-data/instances/{id}/instruments", handleDeleteMarketDataInstrument(svc))
}

// stampSource is middleware that stamps the given source onto the request
// context's caller so the backend attributes mutations to the surface the
// request arrived on. The source is fixed per mount and never read from a
// request header or body. The principal is the placeholder "operator" until
// authentication lands; this is the authorization seam — the resolved principal
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

// handleCreateAccount handles POST /api/v1/accounts.
func handleCreateAccount(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		id := domain.AccountID(req.ID)
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
		limitDTOs := make([]limitDTO, 0, len(limits))
		for _, l := range limits {
			limitDTOs = append(limitDTOs, toLimitDTO(l))
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"account": toAccountDTO(account),
			"limits":  limitDTOs,
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

// handleListLimits handles GET /api/v1/limits[?account=].
func handleListLimits(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		account := domain.AccountID(r.URL.Query().Get("account"))
		limits, err := svc.ListLimits(r.Context(), account)
		if err != nil {
			writeErr(w, err)
			return
		}
		dtos := make([]limitDTO, 0, len(limits))
		for _, l := range limits {
			dtos = append(dtos, toLimitDTO(l))
		}
		writeJSON(w, http.StatusOK, map[string]any{"limits": dtos})
	}
}

// handlePutLimit handles PUT /api/v1/limits.
func handlePutLimit(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req limitDTO
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		limit := fromLimitDTO(req)
		if err := svc.PutLimit(r.Context(), limit); err != nil {
			writeErr(w, err)
			return
		}
		// Re-read back from store is not done here: return the normalized form of
		// what was sent (the backend validates and normalizes; on success the
		// caller knows what was stored).
		writeJSON(w, http.StatusOK, map[string]any{"limit": toLimitDTO(limit)})
	}
}

// handleDeleteLimit handles
// DELETE /api/v1/limits?policy=&scope=&account=&asset=.
func handleDeleteLimit(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		target := domain.LimitTarget{
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

// handleListAudit handles GET /api/v1/audit[?account=&source=&limit=100]. The
// optional account and source filters narrow the trail; trading entities are
// served by the orders/trades/adjustments endpoints, keeping audit and
// trading-ops separated.
func handleListAudit(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, err := limitParam(r, 100, auditCapREST)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		q := r.URL.Query()
		rows, err := svc.ListAuditFiltered(r.Context(),
			domain.AccountID(q.Get("account")), domain.Source(q.Get("source")), n)
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

func handleCreateMarketDataInstance(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req marketDataInstanceDTO
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		instance := domain.MarketDataInstance{
			ID:          req.ID,
			Type:        req.Type,
			Label:       req.Label,
			Credentials: req.Credentials,
			Enabled:     req.Enabled,
		}
		if err := svc.CreateMarketDataInstance(r.Context(), instance); err != nil {
			writeErr(w, err)
			return
		}
		status, err := svc.ListMarketData(r.Context())
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
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
		writeJSON(w, http.StatusOK, map[string]any{"enabled": req.Enabled})
	}
}

func handleDeleteMarketDataInstance(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathID(r)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		if err := svc.DeleteMarketDataInstance(r.Context(), id); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
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
		instrument := domain.MarketDataInstrument{
			InstanceID:     id,
			ExternalSymbol: req.ExternalSymbol,
			BaseAsset:      req.BaseAsset,
			QuoteAsset:     req.QuoteAsset,
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

// handleCreateGroup handles POST /api/v1/groups.
func handleCreateGroup(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID    string `json:"id"`
			Notes string `json:"notes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		group := domain.AccountGroup{ID: req.ID, Notes: req.Notes}
		if err := svc.CreateGroup(r.Context(), group); err != nil {
			writeErr(w, err)
			return
		}
		writeGroup(w, svc, r, req.ID, http.StatusCreated)
	}
}

// handleGetGroup handles GET /api/v1/groups/{id}.
func handleGetGroup(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathID(r)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		group, accounts, err := svc.GetGroup(r.Context(), id)
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

// handleSetGroupNotes handles PUT /api/v1/groups/{id}/notes.
func handleSetGroupNotes(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathID(r)
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
		if err := svc.SetGroupNotes(r.Context(), id, req.Notes); err != nil {
			writeErr(w, err)
			return
		}
		writeGroup(w, svc, r, id, http.StatusOK)
	}
}

// handleBlockGroup handles POST /api/v1/groups/{id}/block.
func handleBlockGroup(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathID(r)
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
		if err := svc.SetGroupBlocked(r.Context(), id, true, req.Reason); err != nil {
			writeErr(w, err)
			return
		}
		writeGroup(w, svc, r, id, http.StatusOK)
	}
}

// handleUnblockGroup handles POST /api/v1/groups/{id}/unblock.
func handleUnblockGroup(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathID(r)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		if err := svc.SetGroupBlocked(r.Context(), id, false, ""); err != nil {
			writeErr(w, err)
			return
		}
		writeGroup(w, svc, r, id, http.StatusOK)
	}
}

// handleDeleteGroup handles DELETE /api/v1/groups/{id}.
func handleDeleteGroup(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathID(r)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		if err := svc.DeleteGroup(r.Context(), id); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// writeGroup re-reads the group and writes it as {"group": {...}} with status,
// so group-mutating handlers return valid JSON reflecting real server state.
// The member accounts returned alongside the group are ignored here.
func writeGroup(w http.ResponseWriter, svc Service, r *http.Request, id string, status int) {
	group, _, err := svc.GetGroup(r.Context(), id)
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
		record, err := svc.ApplyAdjustment(r.Context(), id, fromAdjustmentRequestDTO(req))
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

// handleGetOrder handles GET /api/v1/orders/{id}.
func handleGetOrder(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathInt64(r)
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
		writeJSON(w, http.StatusOK, map[string]any{
			"order":  toOrderDTO(detail.Order),
			"events": events,
			"trades": trades,
		})
	}
}

// handleApplyExecutionReport handles
// POST /api/v1/orders/{id}/execution-reports. The fill's instrument, account,
// and side are taken from the parent order; the body carries only the fill.
func handleApplyExecutionReport(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathInt64(r)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req struct {
			Quantity  string `json:"quantity"`
			Price     string `json:"price"`
			LockPrice string `json:"lockPrice"`
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
			OrderID:      id,
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

// pathID reads the {id} chi path parameter and URL-decodes it. It is the group
// counterpart to pathAccountID.
func pathID(r *http.Request) (string, error) {
	raw := chi.URLParam(r, "id")
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return "", errors.New("invalid URL encoding in id")
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

// pathInt64 reads the {id} chi path parameter as an int64 (order identifier).
func pathInt64(r *http.Request) (int64, error) {
	raw := chi.URLParam(r, "id")
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, errors.New("id must be an integer")
	}
	return v, nil
}

// pathAccountID reads the {id} chi path parameter and URL-decodes it.
func pathAccountID(r *http.Request) (domain.AccountID, error) {
	raw := chi.URLParam(r, "id")
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return "", errors.New("invalid URL encoding in account id")
	}
	return domain.AccountID(decoded), nil
}

// writeErr maps a domain sentinel error to the appropriate HTTP status and
// JSON error body. The codes are: ErrInvalid -> 400 validation, ErrNotFound ->
// 404 not_found, ErrAlreadyExists -> 409 conflict, ErrNotImplemented -> 501
// not_implemented (the message is surfaced so the operator sees which SDK
// capability is missing), and anything else -> 500 internal. The specific
// sentinels take precedence over the generic 500 path.
func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalid):
		writeErrMsg(w, http.StatusBadRequest, "validation", err.Error())
	case errors.Is(err, domain.ErrNotFound):
		writeErrMsg(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, domain.ErrAlreadyExists):
		writeErrMsg(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, domain.ErrNotImplemented):
		writeErrMsg(w, http.StatusNotImplemented, "not_implemented", err.Error())
	default:
		slog.Error("unhandled internal error serving request", "error", err)
		writeErrMsg(w, http.StatusInternalServerError, "internal", "internal error")
	}
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
