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
	"archive/zip"
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/businesscsv"
	"go.openpit.dev/officer/framework/domain"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

func handleExportBackup(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Scope backup.Scope `json:"scope"`
			Zip   bool         `json:"zip"`
		}
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		if !validBackupScope(req.Scope) {
			httpx.WriteValidationProblem(
				w,
				"backup scope must include all or at least one section",
				"/scope",
				"required",
			)
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
func handleExportBusinessCSV(svc backend.ControlPlane) http.HandlerFunc {
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
		if !httpx.DecodeBody(w, r, &req) {
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

// handleRestoreBackup handles POST /api/v1/backup/restore.
func handleRestoreBackup(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Archive         backup.Archive     `json:"archive"`
			ArchiveFile     string             `json:"archiveFile"`
			ArchiveFilename string             `json:"archiveFilename"`
			Scope           backup.Scope       `json:"scope"`
			Mode            backup.RestoreMode `json:"mode"`
		}
		// Deliberately lenient, unlike every other mutation: the body embeds a
		// whole backup.Archive, a portable artifact that another Officer build may
		// have written with fields this build does not know. Rejecting it here
		// would also disagree with the sibling archiveFile path, which decodes the
		// very same document leniently.
		if !httpx.DecodeBodyAllowUnknownFields(w, r, &req) {
			return
		}
		if req.Mode == "" {
			httpx.WriteValidationProblem(
				w, "restore mode is required", "/mode", "required",
			)
			return
		}
		if !validRestoreMode(req.Mode) {
			httpx.WriteValidationProblem(
				w, "unknown restore mode", "/mode", "enum",
			)
			return
		}
		if !validBackupScope(req.Scope) {
			message := "restore scope contains an unknown section"
			constraint := "enum"
			normalized := req.Scope.Normalize()
			if !normalized.All && len(normalized.Sections) == 0 {
				message = "restore scope must include all or at least one section"
				constraint = "required"
			}
			httpx.WriteValidationProblem(
				w, message, "/scope", constraint,
			)
			return
		}
		if req.ArchiveFile == "" && len(req.Archive.Manifest.Sections) == 0 {
			httpx.WriteValidationProblem(
				w, "backup archive is required", "/archive", "required",
			)
			return
		}
		archive := req.Archive
		if req.ArchiveFile != "" {
			parsed, err := parseBackupArchiveFile(req.ArchiveFilename,
				req.ArchiveFile)
			if err != nil {
				httpx.WriteValidationErr(w, err)
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
func handleResetDatabase(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Confirm bool `json:"confirm"`
		}
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		if !req.Confirm {
			httpx.WriteValidationProblem(
				w,
				"database reset confirmation is required",
				"/confirm",
				"required",
			)
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
		return backup.Archive{}, domain.NewValidationError(
			"/archiveFile", "encoding", "invalid backup file encoding",
		)
	}
	if isZipPayload(raw) {
		raw, err = readBackupJSONFromZip(raw)
		if err != nil {
			return backup.Archive{}, err
		}
	}
	var archive backup.Archive
	if !httpx.ValidJSONUnicode(raw) {
		return backup.Archive{}, invalidBackupArchiveJSONError(filename)
	}
	if err := json.Unmarshal(raw, &archive); err != nil {
		return backup.Archive{}, invalidBackupArchiveJSONError(filename)
	}
	return archive, nil
}

func invalidBackupArchiveJSONError(filename string) error {
	if filename == "" {
		return domain.NewValidationError(
			"/archiveFile", "format", "invalid backup archive JSON",
		)
	}
	return domain.NewValidationError(
		"/archiveFile",
		"format",
		fmt.Sprintf("invalid backup archive JSON in %s", filename),
	)
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
		return nil, domain.NewValidationError(
			"/archiveFile", "format", "invalid backup zip archive",
		)
	}
	var candidate *zip.File
	for _, file := range zr.File {
		if file.FileInfo().IsDir() || filepath.Ext(file.Name) != ".json" {
			continue
		}
		if candidate != nil {
			return nil, domain.NewValidationError(
				"/archiveFile", "format",
				"backup zip contains multiple JSON files",
			)
		}
		candidate = file
	}
	if candidate == nil {
		return nil, domain.NewValidationError(
			"/archiveFile", "format", "backup zip contains no JSON archive",
		)
	}
	rc, err := candidate.Open()
	if err != nil {
		return nil, domain.NewValidationError(
			"/archiveFile", "format",
			fmt.Sprintf("open backup JSON from zip: %v", err),
		)
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(io.LimitReader(rc, maxBackupRestoreBody+1))
	if err != nil {
		return nil, domain.NewValidationError(
			"/archiveFile", "format",
			fmt.Sprintf("read backup JSON from zip: %v", err),
		)
	}
	if int64(len(body)) > maxBackupRestoreBody {
		return nil, domain.NewValidationError(
			"/archiveFile", "too_large",
			"backup JSON in zip exceeds size limit",
		)
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
