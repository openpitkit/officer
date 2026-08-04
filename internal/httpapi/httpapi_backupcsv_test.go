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
	"net/http"
	"net/http/httptest"
	"testing"

	"go.openpit.dev/officer/framework/businesscsv"
	"go.openpit.dev/officer/framework/domain"
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
