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
	"errors"
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

func handleExportBackup(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Scope backup.Scope `json:"scope"`
			Zip   bool         `json:"zip"`
		}
		if !httpx.DecodeBody(w, r, &req) {
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

type businessCSVImportPayloadDTO struct {
	Entity        string `json:"entity"`
	Delimiter     string `json:"delimiter"`
	Filename      string `json:"filename"`
	PayloadBase64 string `json:"payloadBase64"`
}

type businessCSVImportRequestDTO struct {
	businessCSVImportPayloadDTO
	ConflictPolicy string `json:"conflictPolicy"`
}

func readBusinessCSVImportRequest(
	w http.ResponseWriter, r *http.Request, requirePolicy bool,
) (backend.BusinessCSVImportRequest, bool) {
	var requestPayload businessCSVImportPayloadDTO
	conflictPolicy := ""
	if requirePolicy {
		var req businessCSVImportRequestDTO
		if !httpx.DecodeBody(w, r, &req) {
			return backend.BusinessCSVImportRequest{}, false
		}
		requestPayload = req.businessCSVImportPayloadDTO
		conflictPolicy = req.ConflictPolicy
	} else if !httpx.DecodeBody(w, r, &requestPayload) {
		return backend.BusinessCSVImportRequest{}, false
	}
	if requestPayload.PayloadBase64 == "" {
		httpx.WriteErrMsg(w, http.StatusBadRequest, "validation",
			"payloadBase64 is required")
		return backend.BusinessCSVImportRequest{}, false
	}
	if requirePolicy && conflictPolicy == "" {
		httpx.WriteErrMsg(w, http.StatusBadRequest, "validation",
			"conflictPolicy is required")
		return backend.BusinessCSVImportRequest{}, false
	}
	payload, err := decodeBusinessCSVPayloadBase64(
		requestPayload.PayloadBase64, businesscsv.MaxImportBytes,
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
		Entity:         businesscsv.Entity(requestPayload.Entity),
		Delimiter:      businesscsv.Delimiter(requestPayload.Delimiter),
		Filename:       requestPayload.Filename,
		Payload:        payload,
		ConflictPolicy: businesscsv.ConflictPolicy(conflictPolicy),
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
		// Deliberately lenient, unlike every other mutation: the body embeds a
		// whole backup.Archive, a portable artifact that another Officer build may
		// have written with fields this build does not know. Rejecting it here
		// would also disagree with the sibling archiveFile path, which decodes the
		// very same document leniently.
		if !httpx.DecodeBodyAllowUnknownFields(w, r, &req) {
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
		if !httpx.DecodeBody(w, r, &req) {
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
		return errors.New("invalid backup archive JSON")
	}
	return fmt.Errorf("invalid backup archive JSON in %s", filename)
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
