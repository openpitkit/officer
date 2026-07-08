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
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/businesscsv"
	"go.openpit.dev/officer/framework/domain"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

func TestBusinessCSVExport(t *testing.T) {
	svc := &fakeService{csvExport: businesscsv.ExportFile{
		Name:        "pit-officer-accounts-20260625T100000Z.csv",
		ContentType: "text/csv; charset=utf-8",
		Body:        []byte("account_id,group_id\nacc-1,desk-a\n"),
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{
		"entity":"accounts",
		"delimiter":"pipe",
		"zip":true,
		"filters":{"groupCode":"desk-a","account":"acc-1","asset":"AAPL","source":"api"}
	}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/business-csv/export", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Disposition"); got !=
		`attachment; filename="pit-officer-accounts-20260625T100000Z.csv"` {
		t.Fatalf("content disposition = %q", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/csv; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}
	if svc.csvExportReq.Entity != businesscsv.EntityAccounts ||
		svc.csvExportReq.Delimiter != businesscsv.DelimiterPipe ||
		!svc.csvExportReq.Zip ||
		svc.csvExportReq.Filter.GroupCode != "desk-a" ||
		!svc.csvExportReq.Filter.GroupCodeSet ||
		svc.csvExportReq.Filter.Account != "acc-1" ||
		svc.csvExportReq.Filter.Asset != "AAPL" ||
		svc.csvExportReq.Filter.Source != domain.SourceAPI {
		t.Fatalf("csvExportReq = %+v", svc.csvExportReq)
	}
}

func TestBusinessCSVExportOmittedGroupFilter(t *testing.T) {
	svc := &fakeService{csvExport: businesscsv.ExportFile{
		Name:        "pit-officer-accounts-20260625T100000Z.csv",
		ContentType: "text/csv; charset=utf-8",
		Body:        []byte("account_id,group_id\n"),
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{
		"entity":"accounts",
		"delimiter":"comma",
		"filters":{"account":"acc-1"}
	}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/business-csv/export", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.csvExportReq.Filter.GroupCodeSet ||
		svc.csvExportReq.Filter.GroupCode != "" {
		t.Fatalf("csvExportReq = %+v", svc.csvExportReq)
	}
}

func TestBusinessCSVExportExplicitEmptyGroupFilter(t *testing.T) {
	svc := &fakeService{csvExport: businesscsv.ExportFile{
		Name:        "pit-officer-accounts-20260625T100000Z.csv",
		ContentType: "text/csv; charset=utf-8",
		Body:        []byte("account_id,group_id\n"),
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{
		"entity":"accounts",
		"delimiter":"comma",
		"filters":{"groupCode":""}
	}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/business-csv/export", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !svc.csvExportReq.Filter.GroupCodeSet ||
		svc.csvExportReq.Filter.GroupCode != "" {
		t.Fatalf("csvExportReq = %+v", svc.csvExportReq)
	}
}

func TestBusinessCSVImport(t *testing.T) {
	svc := &fakeService{csvImport: backend.BusinessCSVImportResult{
		Counts: businesscsv.ImportCounts{Rows: 1, Applied: 1},
		File:   businesscsv.ImportFile{Name: "accounts.csv", Type: "csv"},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	payload := base64.StdEncoding.EncodeToString([]byte(
		"account_id,group_id,notes,blocked,block_reason\nacc-1,,note,false,\n",
	))
	body := bytes.NewBufferString(fmt.Sprintf(`{
		"entity":"accounts",
		"delimiter":"comma",
		"filename":"accounts.csv",
		"payloadBase64":%q,
		"conflictPolicy":"replace"
	}`, payload))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/business-csv/import", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.csvImportReq.Entity != businesscsv.EntityAccounts ||
		svc.csvImportReq.Delimiter != businesscsv.DelimiterComma ||
		svc.csvImportReq.Filename != "accounts.csv" ||
		svc.csvImportReq.ConflictPolicy != businesscsv.ConflictReplace ||
		!bytes.Contains(svc.csvImportReq.Payload, []byte("acc-1")) {
		t.Fatalf("csvImportReq = %+v", svc.csvImportReq)
	}
}

func TestDecodeBusinessCSVPayloadBase64SizeGuard(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		encoded  string
		maxBytes int
		wantErr  bool
	}{
		{
			name:     "pre-decode too large",
			encoded:  "AAAAAAAAA",
			maxBytes: 4,
			wantErr:  true,
		},
		{
			name:     "post-decode too large",
			encoded:  base64.StdEncoding.EncodeToString([]byte("12345")),
			maxBytes: 4,
			wantErr:  true,
		},
		{
			name:     "at limit",
			encoded:  base64.StdEncoding.EncodeToString([]byte("1234")),
			maxBytes: 4,
			wantErr:  false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := decodeBusinessCSVPayloadBase64(tc.encoded, tc.maxBytes)
			if tc.wantErr {
				if !errors.Is(err, domain.ErrTooLarge) {
					t.Fatalf("error = %v, want too large", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeBusinessCSVPayloadBase64: %v", err)
			}
			if string(payload) != "1234" {
				t.Fatalf("payload = %q, want 1234", payload)
			}
		})
	}
}

func TestWriteErrMapsTooLargeTo413(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()

	httpx.WriteErr(rec, businesscsv.NewTooLargeError(false))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != "too_large" ||
		body.Error.Message == "" {
		t.Fatalf("error body = %+v", body.Error)
	}
}

func TestRequestBodyLimitUsesImportEnvelopeCap(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(
		http.MethodPost, "/api/v1/business-csv/import/preview", nil,
	)
	if got := BodyLimitPolicy()(req); got != maxImportBody {
		t.Fatalf("requestBodyLimit import = %d, want %d", got, maxImportBody)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/backup/restore", nil)
	if got := BodyLimitPolicy()(req); got != maxBackupRestoreBody {
		t.Fatalf("requestBodyLimit backup = %d, want %d", got, maxBackupRestoreBody)
	}
}
