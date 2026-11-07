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

package httpapi

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"strconv"
	"time"

	"go.openpit.dev/officer/framework/domain"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

// PreMigrationBackup describes a raw database copy in the list response.
type PreMigrationBackup struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	SizeBytes int64     `json:"sizeBytes"`
}

// PreMigrationBackupSource supplies copies newest first, validated file access
// and audit persistence. Open transfers ownership of the file to the handler.
type PreMigrationBackupSource struct {
	List   func() ([]PreMigrationBackup, error)
	Open   func(name string) (*os.File, int64, error)
	Remove func(name string) error
	Audit  func(context.Context, domain.AuditAction, domain.AuditDetail) error
}

// PreMigrationBackupRoutes serves raw copies outside first-run setup. Downloads
// and deletions validate the copy before recording the request, then act only
// after the audit succeeds.
func PreMigrationBackupRoutes(source PreMigrationBackupSource) []httpx.Route {
	return []httpx.Route{
		{
			ID:      "database.pre-migration-backups.get",
			Method:  http.MethodGet,
			Pattern: "/database/pre-migration-backups",
			Handler: handleListPreMigrationBackups(source),
		},
		{
			ID:      "database.pre-migration-backups.download.post",
			Method:  http.MethodPost,
			Pattern: "/database/pre-migration-backups/{id}/download",
			Handler: handleDownloadPreMigrationBackup(source),
		},
		{
			ID:      "database.pre-migration-backups.delete",
			Method:  http.MethodDelete,
			Pattern: "/database/pre-migration-backups/{id}",
			Handler: handleDeletePreMigrationBackup(source),
		},
	}
}

func handleListPreMigrationBackups(source PreMigrationBackupSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		copies, err := source.List()
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		backups := make([]PreMigrationBackup, 0, len(copies))
		for _, copy := range copies {
			copy.CreatedAt = copy.CreatedAt.UTC()
			backups = append(backups, copy)
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"backups": backups})
	}
}

func handleDownloadPreMigrationBackup(source PreMigrationBackupSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name, err := httpx.PathID(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		file, size, err := source.Open(name)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		defer func() {
			if err := file.Close(); err != nil {
				slog.Error("close pre-migration backup download", "name", name, "error", err)
			}
		}()
		if err := source.Audit(context.WithoutCancel(r.Context()),
			domain.AuditActionDownloadPreMigrationBackup,
			domain.NewAuditDetail(domain.AuditDetailDownloadPreMigrationBackup,
				domain.AuditDetailParams{"name": name}),
		); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		w.Header().Set("Content-Disposition",
			mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		_, _ = io.Copy(w, file)
	}
}

func handleDeletePreMigrationBackup(source PreMigrationBackupSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name, err := httpx.PathID(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		file, _, err := source.Open(name)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		if err := file.Close(); err != nil {
			httpx.WriteErr(w, fmt.Errorf("close pre-migration backup %q: %w", name, err))
			return
		}
		if err := source.Audit(context.WithoutCancel(r.Context()),
			domain.AuditActionDeletePreMigrationBackup,
			domain.NewAuditDetail(domain.AuditDetailDeletePreMigrationBackup,
				domain.AuditDetailParams{"name": name}),
		); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		if err := source.Remove(name); err != nil {
			err = fmt.Errorf("remove pre-migration backup %q: %w", name, err)
			public := httpx.DescribeErr(err)
			if public.Status == http.StatusInternalServerError {
				httpx.WriteErrMsg(w, public.Status, public.Code,
					fmt.Sprintf("could not remove pre-migration backup %q", name))
			} else {
				httpx.WriteErr(w, err)
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
