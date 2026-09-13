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
	"fmt"
	"net/http"
	"net/url"
	"strings"

	backend "go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

func handleListAudit(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		filter, err := auditListFilterFromQuery(q)
		if err != nil {
			httpx.WriteValidationErr(w, err)
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
