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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

type endlessSpaces struct{}

func (endlessSpaces) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	return len(p), nil
}

// --- GET /api/v1/service ----------------------------------------------------

func TestServiceInfo(t *testing.T) {
	svc := &fakeService{
		serviceInfo: backend.ServiceInfo{
			Name:               "Pit Officer",
			EngineVersion:      "v1.2.3",
			EngineBuildProfile: "release",
			Database: backend.ServiceDatabase{
				Path:      "/var/lib/officer.db",
				Reachable: true,
			},
			Release: true,
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/service", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	if m["name"] != "Pit Officer" {
		t.Fatalf("want name=Pit Officer, got %v", m["name"])
	}
	if m["engineVersion"] != "v1.2.3" {
		t.Fatalf("want engineVersion=v1.2.3, got %v", m["engineVersion"])
	}
	if m["engineBuildProfile"] != "release" {
		t.Fatalf("want engineBuildProfile=release, got %v", m["engineBuildProfile"])
	}
	if m["release"] != true {
		t.Fatalf("want release=true, got %v", m["release"])
	}
	db, ok := m["database"].(map[string]any)
	if !ok {
		t.Fatalf("want database object, got %v", m["database"])
	}
	if db["path"] != "/var/lib/officer.db" || db["reachable"] != true {
		t.Fatalf("unexpected database facet: %v", db)
	}
}

// TestServiceInfo_ServiceError checks the /service handler routes a service
// error through writeErr, yielding a generic 500/"internal" (statusErr is the
// injectable error for ServiceInfo).
func TestServiceInfo_ServiceError(t *testing.T) {
	r, _ := newRouter(&fakeService{statusErr: fmt.Errorf("boom")})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/service", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("want code=internal, got %v", errObj["code"])
	}
	if errObj["message"] != "internal error" {
		t.Fatalf("want generic message, got %v", errObj["message"])
	}
}

// --- POST /api/v1/backup/export --------------------------------------------

func TestBackupExport(t *testing.T) {
	createdAt := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	svc := &fakeService{
		backupFilename: "pit-officer-backup-20260622T100000Z.json",
		backupArchive: backup.NewArchive(
			createdAt,
			"test",
			backup.RealmLabel{Code: "test"},
			backup.Scope{All: true},
			backup.Data{Accounts: []backup.Account{{Code: "acc-1"}}},
			backup.CredentialFormPlaintext,
		),
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"scope":{"all":true}}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/backup/export", body,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	disposition := rec.Result().Header.Get("Content-Disposition")
	if disposition != `attachment; filename="pit-officer-backup-20260622T100000Z.json"` {
		t.Fatalf("unexpected content disposition: %q", disposition)
	}
	m := bodyMap(t, rec.Result())
	manifest := m["manifest"].(map[string]any)
	// The manifest is realm-portable: it carries the source realm label and the
	// ordered section list, never a format version.
	realm, ok := manifest["realm"].(map[string]any)
	if !ok || realm["code"] != "test" {
		t.Fatalf("unexpected manifest realm: %v", manifest["realm"])
	}
	if _, ok := manifest["sections"].([]any); !ok {
		t.Fatalf("manifest missing sections: %v", manifest)
	}
}

func TestBackupExportZip(t *testing.T) {
	createdAt := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	svc := &fakeService{
		backupFilename: "pit-officer-backup-20260622T100000Z.json",
		backupArchive: backup.NewArchive(
			createdAt,
			"test",
			backup.RealmLabel{Code: "test"},
			backup.Scope{All: true},
			backup.Data{Accounts: []backup.Account{{Code: "acc-1"}}},
			backup.CredentialFormPlaintext,
		),
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"scope":{"all":true},"zip":true}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/backup/export", body,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if got := rec.Result().Header.Get("Content-Type"); got != "application/zip" {
		t.Fatalf("content type = %q, want application/zip", got)
	}
	disposition := rec.Result().Header.Get("Content-Disposition")
	if disposition != `attachment; filename="pit-officer-backup-20260622T100000Z.zip"` {
		t.Fatalf("unexpected content disposition: %q", disposition)
	}
	zr, err := zip.NewReader(
		bytes.NewReader(rec.Body.Bytes()),
		int64(rec.Body.Len()),
	)
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	if len(zr.File) != 1 || zr.File[0].Name != svc.backupFilename {
		t.Fatalf("unexpected zip files: %+v", zr.File)
	}
	rc, err := zr.File[0].Open()
	if err != nil {
		t.Fatalf("open zip entry: %v", err)
	}
	defer func() { _ = rc.Close() }()
	var archive backup.Archive
	if err := json.NewDecoder(rc).Decode(&archive); err != nil {
		t.Fatalf("decode zip archive: %v", err)
	}
	if archive.Manifest.Realm.Code != "test" ||
		len(archive.Manifest.Sections) == 0 {
		t.Fatalf("unexpected manifest: %+v", archive.Manifest)
	}
}

func TestBackupExport_RequiresScope(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/backup/export",
		bytes.NewBufferString(`{"scope":{}}`),
	))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestBackupExportRejectsUnknownSection(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/backup/export",
		bytes.NewBufferString(`{"scope":{"sections":["unknown"]}}`),
	))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestBackupExportRejectsUnknownNestedScopeField(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/backup/export",
		bytes.NewBufferString(`{
			"scope":{
				"sections":["accounts_groups"],
				"accounts":{"all":true,"ignored":true}
			}
		}`),
	))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestBackupExportServiceErrorReturnsInternal(t *testing.T) {
	r, err := newRouter(&fakeService{backupErr: fmt.Errorf("boom")})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/backup/export",
		bytes.NewBufferString(`{"scope":{"all":true}}`),
	))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
}

// --- POST /api/v1/backup/restore -------------------------------------------

func TestBackupRestoreRequiresMode(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"archive": backup.Archive{},
		"scope":   backup.Scope{All: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/backup/restore", bytes.NewReader(body),
	))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestBackupRestoreRejectsUnknownMode(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"archive": backup.Archive{},
		"scope":   backup.Scope{All: true},
		"mode":    "bogus",
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/backup/restore", bytes.NewReader(body),
	))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestBackupRestore(t *testing.T) {
	summary := backup.NewSummary()
	summary.AddApplied(backup.SectionAccountsGroups, 1)
	svc := &fakeService{backupSummary: summary}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	source := "  raw test source  "
	archive := backup.NewArchive(
		time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC),
		source,
		backup.RealmLabel{Code: "test"},
		backup.Scope{All: true},
		backup.Data{},
		backup.CredentialFormPlaintext,
	)
	body, err := json.Marshal(map[string]any{
		"archive": archive,
		"scope":   backup.Scope{All: true},
		"mode":    backup.RestoreModeOverwrite,
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/backup/restore", bytes.NewReader(body),
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	got := m["summary"].(map[string]any)
	applied := got["applied"].(map[string]any)
	if applied[string(backup.SectionAccountsGroups)] != float64(1) {
		t.Fatalf("unexpected summary: %v", got)
	}
	if svc.restoreArchive.Manifest.Source != source ||
		svc.restoreOptions.Mode != backup.RestoreModeOverwrite ||
		!svc.restoreOptions.Scope.All {
		t.Fatalf("restore call = archive %+v options %+v",
			svc.restoreArchive.Manifest, svc.restoreOptions)
	}
}

func TestBackupRestoreRejectsInvalidJSONFraming(t *testing.T) {
	archive := backup.NewArchive(
		time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC),
		"test",
		backup.RealmLabel{Code: "test"},
		backup.Scope{All: true},
		backup.Data{},
		backup.CredentialFormPlaintext,
	)
	body, err := json.Marshal(map[string]any{
		"archive": archive,
		"scope":   backup.Scope{All: true},
		"mode":    backup.RestoreModeOverwrite,
	})
	if err != nil {
		t.Fatal(err)
	}
	invalidUTF8 := bytes.Clone(body)
	needle := []byte(`"source":"test"`)
	index := bytes.Index(invalidUTF8, needle)
	if index < 0 {
		t.Fatalf("source field not found in %s", body)
	}
	invalidUTF8[index+len(`"source":"`)] = 0xff
	invalidSurrogate := bytes.Replace(
		body, needle, []byte(`"source":"\ud800"`), 1,
	)
	tests := []struct {
		name string
		body []byte
	}{
		{
			"trailing value",
			append(bytes.Clone(body), []byte(`{"ignored":true}`)...),
		},
		{"invalid UTF-8", invalidUTF8},
		{"invalid UTF-16 surrogate", invalidSurrogate},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(
				http.MethodPost, "/api/v1/backup/restore", bytes.NewReader(tc.body),
			))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d body=%s", rec.Code, rec.Body.String())
			}
			if svc.restoreOptions.Mode != "" {
				t.Fatalf("RestoreBackup was called with options %+v",
					svc.restoreOptions)
			}
		})
	}
}

func TestBackupRestoreJSONFile(t *testing.T) {
	summary := backup.NewSummary()
	summary.AddApplied(backup.SectionAccountsGroups, 1)
	svc := &fakeService{backupSummary: summary}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	archive := backup.NewArchive(
		time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC),
		"json-file-test",
		backup.RealmLabel{Code: "test"},
		backup.Scope{All: true},
		backup.Data{Accounts: []backup.Account{{Code: "acc-json"}}},
		backup.CredentialFormPlaintext,
	)
	payload, err := json.Marshal(archive)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"archiveFile":     base64.StdEncoding.EncodeToString(payload),
		"archiveFilename": "pit-officer-backup-20260622T100000Z.json",
		"scope":           backup.Scope{All: true},
		"mode":            backup.RestoreModeReplaceAll,
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/backup/restore", bytes.NewReader(body),
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if svc.restoreArchive.Manifest.Source != "json-file-test" ||
		len(svc.restoreArchive.Data.Accounts) != 1 ||
		svc.restoreArchive.Data.Accounts[0].Code != "acc-json" ||
		svc.restoreOptions.Mode != backup.RestoreModeReplaceAll ||
		!svc.restoreOptions.Scope.All {
		t.Fatalf("restore call = archive %+v options %+v",
			svc.restoreArchive, svc.restoreOptions)
	}
}

func TestBackupRestoreZipFile(t *testing.T) {
	summary := backup.NewSummary()
	summary.AddApplied(backup.SectionAccountsGroups, 1)
	svc := &fakeService{backupSummary: summary}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	archive := backup.NewArchive(
		time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC),
		"test",
		backup.RealmLabel{Code: "test"},
		backup.Scope{All: true},
		backup.Data{Accounts: []backup.Account{{Code: "acc-zip"}}},
		backup.CredentialFormPlaintext,
	)
	payload, filename, err := zipBackupArchive(
		archive,
		"pit-officer-backup-20260622T100000Z.json",
	)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"archiveFile":     base64.StdEncoding.EncodeToString(payload),
		"archiveFilename": filename,
		"scope":           backup.Scope{All: true},
		"mode":            backup.RestoreModeOverwrite,
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/backup/restore", bytes.NewReader(body),
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	got := m["summary"].(map[string]any)
	applied := got["applied"].(map[string]any)
	if applied[string(backup.SectionAccountsGroups)] != float64(1) {
		t.Fatalf("unexpected summary: %v", got)
	}
	if svc.restoreArchive.Manifest.Source != "test" ||
		len(svc.restoreArchive.Data.Accounts) != 1 ||
		svc.restoreArchive.Data.Accounts[0].Code != "acc-zip" ||
		svc.restoreOptions.Mode != backup.RestoreModeOverwrite ||
		!svc.restoreOptions.Scope.All {
		t.Fatalf("restore call = archive %+v options %+v",
			svc.restoreArchive, svc.restoreOptions)
	}
}

func TestBackupRestoreFileValidation(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		encoded  string
		want     string
	}{
		{
			name:     "invalid base64",
			filename: "backup.json",
			encoded:  "not base64",
			want:     "invalid backup file encoding",
		},
		{
			name:     "corrupt zip",
			filename: "backup.zip",
			encoded: base64.StdEncoding.EncodeToString(
				[]byte{'P', 'K', 0x03, 0x04, 'x'},
			),
			want: "invalid backup zip archive",
		},
		{
			name:     "zip without json",
			filename: "backup.zip",
			encoded: base64.StdEncoding.EncodeToString(
				testZipPayload(t, map[string]string{"notes.txt": "x"}),
			),
			want: "backup zip contains no JSON archive",
		},
		{
			name:     "zip with multiple json files",
			filename: "backup.zip",
			encoded: base64.StdEncoding.EncodeToString(
				testZipPayload(t, map[string]string{
					"one.json": "{}",
					"two.json": "{}",
				}),
			),
			want: "backup zip contains multiple JSON files",
		},
		{
			name:     "oversized zip json",
			filename: "backup.zip",
			encoded: base64.StdEncoding.EncodeToString(
				testZipPayload(t, map[string]string{
					"backup.json": strings.Repeat(" ", int(maxBackupRestoreBody)+1),
				}),
			),
			want: "backup JSON in zip exceeds size limit",
		},
		{
			name:     "invalid json file",
			filename: "backup.json",
			encoded:  base64.StdEncoding.EncodeToString([]byte("{")),
			want:     "invalid backup archive JSON in backup.json",
		},
		{
			name:     "invalid UTF-8 json file",
			filename: "backup.json",
			encoded: base64.StdEncoding.EncodeToString(
				[]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'},
			),
			want: "invalid backup archive JSON in backup.json",
		},
		{
			name:     "invalid UTF-16 surrogate json file",
			filename: "backup.json",
			encoded: base64.StdEncoding.EncodeToString(
				[]byte(`{"x":"\ud800"}`),
			),
			want: "invalid backup archive JSON in backup.json",
		},
		{
			name:    "invalid json file without filename",
			encoded: base64.StdEncoding.EncodeToString([]byte("{")),
			want:    "invalid backup archive JSON",
		},
		{
			name:     "invalid json in zip",
			filename: "backup.zip",
			encoded: base64.StdEncoding.EncodeToString(
				testZipPayload(t, map[string]string{"backup.json": "{"}),
			),
			want: "invalid backup archive JSON in backup.zip",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeService{}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(map[string]any{
				"archiveFile":     tt.encoded,
				"archiveFilename": tt.filename,
				"scope":           backup.Scope{All: true},
				"mode":            backup.RestoreModeOverwrite,
			})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(
				http.MethodPost, "/api/v1/backup/restore", bytes.NewReader(body),
			))
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("want 400, got %d", rec.Code)
			}
			errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
			if errObj["code"] != "validation" || errObj["message"] != tt.want {
				t.Fatalf("error = %v, want validation %q", errObj, tt.want)
			}
			if svc.restoreOptions.Mode != "" {
				t.Fatalf("RestoreBackup was called with options %+v",
					svc.restoreOptions)
			}
		})
	}
}

func TestBackupRestoreInvalidArchiveReturnsBadRequest(t *testing.T) {
	r, err := newRouter(&fakeService{
		backupErr: fmt.Errorf("bad backup: %w", domain.ErrInvalid),
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"archive": backup.NewArchive(
			time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC),
			"test",
			backup.RealmLabel{Code: "test"},
			backup.Scope{All: true},
			backup.Data{},
			backup.CredentialFormPlaintext,
		),
		"scope": backup.Scope{All: true},
		"mode":  backup.RestoreModeOverwrite,
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/backup/restore", bytes.NewReader(body),
	))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestBackupRestoreRequiresArchive(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"scope": backup.Scope{All: true},
		"mode":  backup.RestoreModeOverwrite,
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/backup/restore", bytes.NewReader(body),
	))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "validation" ||
		errObj["message"] != "backup archive is required" {
		t.Fatalf("unexpected error: %v", errObj)
	}
	if svc.restoreOptions.Mode != "" {
		t.Fatalf("RestoreBackup was called with options %+v",
			svc.restoreOptions)
	}
}

func TestBackupRestoreRejectsEmptyScope(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"archive": backup.NewArchive(
			time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC),
			"test",
			backup.RealmLabel{Code: "test"},
			backup.Scope{All: true},
			backup.Data{},
			backup.CredentialFormPlaintext,
		),
		"scope": backup.Scope{},
		"mode":  backup.RestoreModeOverwrite,
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/backup/restore", bytes.NewReader(body),
	))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "validation" ||
		errObj["message"] !=
			"restore scope must include all or at least one section" {
		t.Fatalf("unexpected error: %v", errObj)
	}
	if svc.restoreOptions.Mode != "" {
		t.Fatalf("RestoreBackup was called with options %+v",
			svc.restoreOptions)
	}
}

func TestBackupRestoreFilePayloadOverridesInlineArchive(t *testing.T) {
	svc := &fakeService{backupSummary: backup.NewSummary()}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	inline := backup.NewArchive(
		time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC),
		"inline",
		backup.RealmLabel{Code: "test"},
		backup.Scope{All: true},
		backup.Data{},
		backup.CredentialFormPlaintext,
	)
	fileArchive := backup.NewArchive(
		time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC),
		"file",
		backup.RealmLabel{Code: "test"},
		backup.Scope{All: true},
		backup.Data{},
		backup.CredentialFormPlaintext,
	)
	payload, err := json.Marshal(fileArchive)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"archive":         inline,
		"archiveFile":     base64.StdEncoding.EncodeToString(payload),
		"archiveFilename": "backup.json",
		"scope":           backup.Scope{All: true},
		"mode":            backup.RestoreModeOverwrite,
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/backup/restore", bytes.NewReader(body),
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if svc.restoreArchive.Manifest.Source != "file" {
		t.Fatalf("restore archive source = %q, want file",
			svc.restoreArchive.Manifest.Source)
	}
}

// TestBackupRestoreParsingConvergesAcrossShapes checks the handler's parsing
// convergence: an inline archive, a base64-encoded JSON file, and a
// base64-encoded ZIP file all reach svc.RestoreBackup as the identical parsed
// archive - compared whole, not by a couple of manifest fields, so a shape
// that silently drops Data or Manifest.Realm or Manifest.CreatedAt would fail
// it. The comparison marshals both sides back to JSON rather than using
// reflect.DeepEqual on the Go structs: Archive is defined (see backup.go) as
// "the single JSON document copied between Officer realms", and its slice
// fields carry `omitempty`, so a field FilterData leaves as a non-nil empty
// slice (e.g. Data.Balances) marshals identically to, and is indistinguishable
// on the wire from, one left nil - a difference reflect.DeepEqual would flag
// but which is not a parsing defect. It does not exercise the manifest-source
// validation rule itself - the fake service cannot; that rule lives in the
// backend package and is covered by its own tests.
func TestBackupRestoreParsingConvergesAcrossShapes(t *testing.T) {
	archive := backup.NewArchive(
		time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC),
		"  convergence test source  ",
		backup.RealmLabel{Code: "test"},
		backup.Scope{All: true},
		backup.Data{Accounts: []backup.Account{{Code: "acc-converge"}}},
		backup.CredentialFormPlaintext,
	)
	jsonPayload, err := json.Marshal(archive)
	if err != nil {
		t.Fatal(err)
	}
	zipPayload, zipFilename, err := zipBackupArchive(archive, "backup.json")
	if err != nil {
		t.Fatal(err)
	}

	shapes := []struct {
		name string
		body map[string]any
	}{
		{
			name: "inline archive",
			body: map[string]any{
				"archive": archive,
				"scope":   backup.Scope{All: true},
				"mode":    backup.RestoreModeOverwrite,
			},
		},
		{
			name: "base64 JSON file",
			body: map[string]any{
				"archiveFile":     base64.StdEncoding.EncodeToString(jsonPayload),
				"archiveFilename": "backup.json",
				"scope":           backup.Scope{All: true},
				"mode":            backup.RestoreModeOverwrite,
			},
		},
		{
			name: "base64 ZIP file",
			body: map[string]any{
				"archiveFile":     base64.StdEncoding.EncodeToString(zipPayload),
				"archiveFilename": zipFilename,
				"scope":           backup.Scope{All: true},
				"mode":            backup.RestoreModeOverwrite,
			},
		},
	}
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			svc := &fakeService{backupSummary: backup.NewSummary()}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(shape.body)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(
				http.MethodPost, "/api/v1/backup/restore", bytes.NewReader(body),
			))
			if rec.Code != http.StatusOK {
				t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
			}
			gotJSON, err := json.MarshalIndent(svc.restoreArchive, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			wantJSON, err := json.MarshalIndent(archive, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("parsed archive does not match submitted archive:\n"+
					"got:\n%s\nwant:\n%s", gotJSON, wantJSON)
			}
		})
	}
}

func testZipPayload(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", name, err)
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Fatalf("write zip entry %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func TestBackupRestoreServiceErrorReturnsInternal(t *testing.T) {
	r, err := newRouter(&fakeService{backupErr: fmt.Errorf("boom")})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"archive": backup.NewArchive(
			time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC),
			"test",
			backup.RealmLabel{Code: "test"},
			backup.Scope{All: true},
			backup.Data{},
			backup.CredentialFormPlaintext,
		),
		"scope": backup.Scope{All: true},
		"mode":  backup.RestoreModeOverwrite,
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/backup/restore", bytes.NewReader(body),
	))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
}

func TestBackupRestoreBodyLimit(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/backup/restore",
		io.LimitReader(endlessSpaces{}, maxBackupRestoreBody+1),
	))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestOrdinaryEndpointBodyLimitRemainsSmall(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/accounts",
		strings.NewReader(strings.Repeat(" ", maxRequestBody+1)),
	))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestResetDatabaseRequiresConfirmation(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"confirm": false})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/database/reset", bytes.NewReader(body),
	))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "validation" ||
		errObj["message"] != "database reset confirmation is required" {
		t.Fatalf("unexpected error: %v", errObj)
	}
	if svc.resetCalled {
		t.Fatalf("ResetDatabase was called without confirmation")
	}
}

func TestResetDatabase(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"confirm": true})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/database/reset", bytes.NewReader(body),
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if !svc.resetCalled {
		t.Fatalf("ResetDatabase was not called")
	}
	m := bodyMap(t, rec.Result())
	if m["ok"] != true {
		t.Fatalf("unexpected response: %v", m)
	}
}

func TestResetDatabaseServiceErrorReturnsInternal(t *testing.T) {
	r, err := newRouter(&fakeService{resetErr: fmt.Errorf("boom")})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"confirm": true})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/database/reset", bytes.NewReader(body),
	))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
}

// --- GET /api/v1/overview ---------------------------------------------------

func TestOverview(t *testing.T) {
	at := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	svc := &fakeService{
		overview: backend.Overview{
			Counts: backend.Counts{
				Accounts:       3,
				AccountsActive: 2,
				Groups:         4,
				GroupsActive:   3,
				Limits:         5,
				OrdersActive:   6,
				OrdersToday:    2,
				OrdersTotal:    9,
			},
			Activity: []backend.Activity{
				{
					At:      at,
					Source:  domain.SourceAPI,
					Kind:    backend.ActivityKindOrder,
					Ref:     "7",
					Summary: "order acc-1 buy AAPL",
				},
			},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	counts, ok := m["counts"].(map[string]any)
	if !ok {
		t.Fatalf("want counts object, got %v", m["counts"])
	}
	// JSON numbers decode to float64.
	if counts["accounts"] != float64(3) ||
		counts["accountsActive"] != float64(2) ||
		counts["groups"] != float64(4) ||
		counts["groupsActive"] != float64(3) ||
		counts["ordersActive"] != float64(6) ||
		counts["ordersTotal"] != float64(9) {
		t.Fatalf("unexpected counts: %v", counts)
	}
	activity, ok := m["activity"].([]any)
	if !ok || len(activity) != 1 {
		t.Fatalf("want 1 activity entry, got %v", m["activity"])
	}
	a := activity[0].(map[string]any)
	for _, field := range []string{"at", "source", "kind", "ref", "summary"} {
		if _, ok := a[field]; !ok {
			t.Fatalf("activity missing field %q", field)
		}
	}
}

// TestOverview_Since exercises both the well-formed and the unparseable ?since=
// parameter. Either way the handler must answer 200: a usable RFC3339 value
// sets the "today" boundary, an unparseable one silently falls back to the
// server-local start of day. The fake ignores the boundary, so only the status
// code is asserted.
func TestOverview_Since(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{name: "valid RFC3339", query: "?since=2026-06-11T00:00:00Z"},
		{name: "unparseable fallback", query: "?since=not-a-time"},
		{name: "empty value fallback", query: "?since="},
		{name: "absent", query: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := newRouter(&fakeService{})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
				"/api/v1/overview"+tc.query, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("want 200, got %d", rec.Code)
			}
		})
	}
}

// TestOverview_ServiceError checks the overview handler maps a service error
// through writeErr to a 500 (statusErr is the injectable error for Overview).
// Unlike /status this path does NOT degrade to 503; it is a genuine error.
func TestOverview_ServiceError(t *testing.T) {
	r, _ := newRouter(&fakeService{statusErr: fmt.Errorf("boom")})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("want code=internal, got %v", errObj["code"])
	}
}

// --- GET /api/v1/status (service-error 503 path) ----------------------------

// TestV1Status_ServiceUnavailable checks the special-cased /status path: on a
// Status error the handler does NOT go through writeErr. It returns 503 with a
// degraded statusDTO (healthy:false, a zero node, no "error" object).
func TestV1Status_ServiceUnavailable(t *testing.T) {
	r, err := newRouter(&fakeService{statusErr: fmt.Errorf("store unreachable")})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	if m["healthy"] != false {
		t.Fatalf("want healthy:false, got %v", m["healthy"])
	}
	node, ok := m["node"].(map[string]any)
	if !ok {
		t.Fatalf("want a node object, got %v", m["node"])
	}
	if engine, ok := node["engine"].(map[string]any); !ok || engine["running"] != false {
		t.Fatalf("want a degraded node with engine.running:false, got %v", node)
	}
	// The degraded body must not carry the writeErr "error" envelope.
	if _, present := m["error"]; present {
		t.Fatalf("503 status body must not contain an error object: %v", m)
	}
}

// --- GET /api/v1/accounts (service-error path) ------------------------------

// TestListAccounts_ServiceError drives the harness-added listAccountsErr to the
// list handler's 500 branch. The happy path is covered by TestListAccounts.
func TestListAccounts_ServiceError(t *testing.T) {
	r, _ := newRouter(&fakeService{listAccountsErr: fmt.Errorf("boom")})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/accounts", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("want code=internal, got %v", errObj["code"])
	}
}

// TestListAccounts_Empty checks the empty case returns a non-null JSON array,
// not null, so the SPA can iterate it unconditionally.
func TestListAccounts_Empty(t *testing.T) {
	r, _ := newRouter(&fakeService{})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/accounts", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	accounts, ok := m["accounts"].([]any)
	if !ok {
		t.Fatalf("want accounts array, got %v", m["accounts"])
	}
	if len(accounts) != 0 {
		t.Fatalf("want empty array, got %v", accounts)
	}
}

// --- POST /api/v1/accounts/{id}/unblock (error/not-found paths) -------------

// TestUnblockAccount_NotFound checks an unblockErr that is domain.ErrNotFound
// maps to 404. The happy path is covered by TestUnblockAccount.
func TestUnblockAccount_NotFound(t *testing.T) {
	r, err := newRouter(&fakeService{unblockErr: domain.ErrNotFound})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/accounts/acc-x/unblock?missingAccount=create", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "not_found" {
		t.Fatalf("want code=not_found, got %v", errObj["code"])
	}
}

// TestUnblockAccount_ServiceError checks a non-sentinel unblockErr maps to a
// generic 500/"internal".
func TestUnblockAccount_ServiceError(t *testing.T) {
	r, _ := newRouter(&fakeService{unblockErr: fmt.Errorf("boom")})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/accounts/acc-1/unblock?missingAccount=create", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("want code=internal, got %v", errObj["code"])
	}
}

// --- GET /api/v1/balances ---------------------------------------------------

func TestListBalances(t *testing.T) {
	updated := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	svc := &fakeService{
		balances: []domain.Balance{
			{
				UpdatedAt:         updated,
				Account:           "acc-1",
				Asset:             "USD",
				Available:         "1000",
				Held:              "50",
				Incoming:          "0",
				RealizedPnl:       "12",
				AverageEntryPrice: "",
			},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/balances", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	balances, ok := m["balances"].([]any)
	if !ok || len(balances) != 1 {
		t.Fatalf("want 1 balance, got %v", m["balances"])
	}
	b := balances[0].(map[string]any)
	for _, field := range []string{
		"updatedAt", "account", "asset", "available", "held",
		"incoming", "realizedPnl", "averageEntryPrice",
	} {
		if _, ok := b[field]; !ok {
			t.Fatalf("balance missing field %q", field)
		}
	}
	if b["account"] != "acc-1" || b["asset"] != "USD" || b["available"] != "1000" {
		t.Fatalf("unexpected balance: %v", b)
	}
}

// TestListBalances_QueryParams checks the account/asset query parameters are
// accepted and the handler still answers 200. The fake ignores the filter
// arguments, so this asserts the parse-and-forward path, not the filtering.
func TestListBalances_QueryParams(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{name: "account only", query: "?account=acc-1"},
		{name: "asset only", query: "?asset=USD"},
		{name: "account and asset", query: "?account=acc-1&asset=USD"},
		{name: "no filter", query: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := newRouter(&fakeService{})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
				"/api/v1/balances"+tc.query, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("want 200, got %d", rec.Code)
			}
			m := bodyMap(t, rec.Result())
			if _, ok := m["balances"].([]any); !ok {
				t.Fatalf("want balances array, got %v", m["balances"])
			}
		})
	}
}

// TestListBalances_ServiceError drives the harness-added balancesErr to the
// list handler's 500 branch.
func TestListBalances_ServiceError(t *testing.T) {
	r, _ := newRouter(&fakeService{balancesErr: fmt.Errorf("boom")})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/balances", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("want code=internal, got %v", errObj["code"])
	}
}

func TestListBalances_CurrencyRequiredIsValidation(t *testing.T) {
	r, _ := newRouter(&fakeService{balancesErr: store.ErrCurrencyRequired})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/balances", nil))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

// --- PUT /api/v1/accounts/{id}/group ----------------------------------------

func TestSetAccountGroup(t *testing.T) {
	// writeAccount re-reads the account via GetAccountState, so the account
	// must be seeded or the success response would itself 404.
	svc := &fakeService{
		accounts: []domain.Account{{Code: "acc-1"}},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"group":"vip"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/accounts/acc-1/group?missingAccount=create", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	acc, ok := m["account"].(map[string]any)
	if !ok || acc["code"] != "acc-1" {
		t.Fatalf("want account object for acc-1, got %v", m["account"])
	}
}

// TestSetAccountGroup_ClearMembership checks an empty group clears membership
// and still answers 200 (the handler documents the empty-group clear case).
func TestSetAccountGroup_ClearMembership(t *testing.T) {
	svc := &fakeService{
		accounts: []domain.Account{{Code: "acc-1"}},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"group":""}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/accounts/acc-1/group?missingAccount=create", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestSetAccountGroup_InvalidJSON(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{bad`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/accounts/acc-1/group?missingAccount=create", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

// TestSetAccountGroup_NotFound checks a stateErr of domain.ErrNotFound (e.g. an
// unknown account or target group) maps to 404. stateErr is the injectable
// error for SetAccountGroup.
func TestSetAccountGroup_NotFound(t *testing.T) {
	r, err := newRouter(&fakeService{stateErr: domain.ErrNotFound})
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"group":"vip"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/accounts/acc-x/group?missingAccount=create", body))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "not_found" {
		t.Fatalf("want code=not_found, got %v", errObj["code"])
	}
}

// TestSetAccountGroup_ServiceError checks a non-sentinel stateErr maps to a
// generic 500/"internal".
func TestSetAccountGroup_ServiceError(t *testing.T) {
	r, _ := newRouter(&fakeService{stateErr: fmt.Errorf("boom")})
	body := bytes.NewBufferString(`{"group":"vip"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/accounts/acc-1/group?missingAccount=create", body))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("want code=internal, got %v", errObj["code"])
	}
}

// --- PUT /api/v1/accounts/{id}/notes ----------------------------------------

func TestSetAccountNotes(t *testing.T) {
	svc := &fakeService{
		accounts: []domain.Account{{Code: "acc-1"}},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"notes":"watch closely"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/accounts/acc-1/notes", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	acc, ok := m["account"].(map[string]any)
	if !ok || acc["code"] != "acc-1" {
		t.Fatalf("want account object for acc-1, got %v", m["account"])
	}
}

// TestSetAccountNotes_Empty checks clearing notes (empty string) still answers
// 200.
func TestSetAccountNotes_Empty(t *testing.T) {
	svc := &fakeService{
		accounts: []domain.Account{{Code: "acc-1"}},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"notes":""}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/accounts/acc-1/notes", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestSetAccountNotes_InvalidJSON(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{bad`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/accounts/acc-1/notes", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

// TestSetAccountNotes_NotFound checks a stateErr of domain.ErrNotFound (unknown
// account) maps to 404.
func TestSetAccountNotes_NotFound(t *testing.T) {
	r, err := newRouter(&fakeService{stateErr: domain.ErrNotFound})
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"notes":"x"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/accounts/acc-x/notes", body))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "not_found" {
		t.Fatalf("want code=not_found, got %v", errObj["code"])
	}
}

// TestSetAccountNotes_ServiceError checks a non-sentinel stateErr maps to a
// generic 500/"internal".
func TestSetAccountNotes_ServiceError(t *testing.T) {
	r, _ := newRouter(&fakeService{stateErr: fmt.Errorf("boom")})
	body := bytes.NewBufferString(`{"notes":"x"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/accounts/acc-1/notes", body))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("want code=internal, got %v", errObj["code"])
	}
}
