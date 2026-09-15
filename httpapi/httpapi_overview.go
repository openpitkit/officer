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
	"net/http"
	"strings"
	"time"

	"go.openpit.dev/officer/framework/backend"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

func handleOverview(svc backend.ControlPlane) http.HandlerFunc {
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
func handleServiceInfo(svc backend.ControlPlane) http.HandlerFunc {
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
