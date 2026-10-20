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
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
)

// noSealReport is the SealReportSource of a credential store never sealed.
func noSealReport(context.Context) (time.Time, int, bool, error) {
	return time.Time{}, 0, false, nil
}

func getCredentialStorage(t *testing.T, svc *fakeService) *httptest.ResponseRecorder {
	t.Helper()
	return getCredentialStorageSealed(t, svc, noSealReport)
}

// getCredentialStorageSealed serves GET /credential-storage over svc with the
// seal reports of sealReports.
func getCredentialStorageSealed(
	t *testing.T, svc *fakeService, sealReports SealReportSource,
) *httptest.ResponseRecorder {
	t.Helper()
	r, err := newRouterOver(svc, nil, sealReports)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/credential-storage", nil))
	return rec
}

func TestGetCredentialStorage(t *testing.T) {
	const neverSealed = `"sealedAt":null,"resealedSecrets":0}`
	for _, tc := range []struct {
		name    string
		storage backend.CredentialStorage
		want    string
	}{
		{"sealed", backend.CredentialStorage{Sealed: true}, `{"credentialsSealed":true,"plaintextTradingKeys":false,"plaintextMarketDataKeys":false,` + neverSealed},
		{"unsealed-trading-keys", backend.CredentialStorage{PlaintextTradingKeys: true}, `{"credentialsSealed":false,"plaintextTradingKeys":true,"plaintextMarketDataKeys":false,` + neverSealed},
		{"unsealed-market-data-keys", backend.CredentialStorage{PlaintextMarketDataKeys: true}, `{"credentialsSealed":false,"plaintextTradingKeys":false,"plaintextMarketDataKeys":true,` + neverSealed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := getCredentialStorage(t, &fakeService{credentialStorage: tc.storage})
			if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || got != tc.want {
				t.Fatalf("GET /credential-storage = %d %s, want 200 %s", rec.Code, got, tc.want)
			}
		})
	}
}

// TestGetCredentialStorage_SealReport checks the seal report fields of the
// REST answer: null and 0 before the store is sealed, and after it the seal
// time in UTC and how many secrets the seal resealed.
func TestGetCredentialStorage_SealReport(t *testing.T) {
	svc := &fakeService{credentialStorage: backend.CredentialStorage{Sealed: true}}
	const storage = `{"credentialsSealed":true,"plaintextTradingKeys":false,"plaintextMarketDataKeys":false,`
	sealedAt := time.Date(2026, 3, 4, 5, 6, 7, 0, time.FixedZone("UTC+2", 2*60*60))
	for _, tc := range []struct {
		name        string
		sealReports SealReportSource
		want        string
	}{
		{"before sealing", noSealReport,
			storage + `"sealedAt":null,"resealedSecrets":0}`},
		{"after sealing", func(context.Context) (time.Time, int, bool, error) {
			return sealedAt, 3, true, nil
		}, storage + `"sealedAt":"2026-03-04T03:06:07Z","resealedSecrets":3}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := getCredentialStorageSealed(t, svc, tc.sealReports)
			if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || got != tc.want {
				t.Fatalf("GET /credential-storage = %d %s, want 200 %s", rec.Code, got, tc.want)
			}
		})
	}
}

// TestGetCredentialStorage_ServiceError checks that a service error goes
// through WriteErr: an unclassified one answers the generic 500 envelope.
func TestGetCredentialStorage_ServiceError(t *testing.T) {
	rec := getCredentialStorage(t, &fakeService{credentialStorageErr: errors.New("boom")})
	want := `{"error":{"code":"internal","message":"internal error"}}`
	if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusInternalServerError || got != want {
		t.Fatalf("GET /credential-storage = %d %s, want 500 %s", rec.Code, got, want)
	}
}

// TestGetCredentialStorage_SealReportError checks that a seal report that
// cannot be read fails the request with the generic 500 envelope instead of
// answering without the report.
func TestGetCredentialStorage_SealReportError(t *testing.T) {
	rec := getCredentialStorageSealed(t, &fakeService{},
		func(context.Context) (time.Time, int, bool, error) {
			return time.Time{}, 0, false, errors.New("seal report unreadable")
		})
	want := `{"error":{"code":"internal","message":"internal error"}}`
	if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusInternalServerError || got != want {
		t.Fatalf("GET /credential-storage = %d %s, want 500 %s", rec.Code, got, want)
	}
}

// TestRegisterRoutesRefusesNilSealReports checks that the route table is not
// built without a seal report source.
func TestRegisterRoutesRefusesNilSealReports(t *testing.T) {
	defer func() {
		got := recover()
		if got != "httpapi: RegisterRoutes needs a seal report source" {
			t.Fatalf("NewRouteRegistry with a nil seal report source panicked with %v", got)
		}
	}()
	NewRouteRegistry(&fakeService{}, nil, nil)
}

// TestGetCredentialStorage_InvalidStoredCredentials checks that a service
// error wrapping domain.ErrInvalid, as a keyed market-data source's malformed
// stored credentials yield, answers the 422 problem+json envelope.
func TestGetCredentialStorage_InvalidStoredCredentials(t *testing.T) {
	const cause = "backend: market-data source md-1: stored market-data credentials are not a JSON object"
	rec := getCredentialStorage(t, &fakeService{
		credentialStorageErr: fmt.Errorf("%s: %w", cause, domain.ErrInvalid),
	})
	want := `{"type":"about:blank","title":"Unprocessable Content","status":422,` +
		`"detail":"` + cause + `: invalid","errors":[{"code":"validation","pointer":""}]}`
	got := strings.TrimSpace(rec.Body.String())
	if contentType := rec.Header().Get("Content-Type"); rec.Code != http.StatusUnprocessableEntity ||
		contentType != "application/problem+json" || got != want {
		t.Fatalf("GET /credential-storage = %d %s %s, want 422 application/problem+json %s",
			rec.Code, contentType, got, want)
	}
}

func TestOpenAPICredentialStorageContract(t *testing.T) {
	spec := string(openapiSpec)
	if !strings.Contains(spec, "\n  /credential-storage:\n    get:\n") {
		t.Fatal("GET /credential-storage is undocumented")
	}
	wantRequired, wantProperties := jsonMembers(reflect.TypeOf(credentialStorageDTO{}))
	gotRequired, gotProperties := openAPISchemaMembers(t, spec, "CredentialStorage")
	if !reflect.DeepEqual(gotRequired, wantRequired) || !reflect.DeepEqual(gotProperties, wantProperties) {
		t.Fatalf("OpenAPI CredentialStorage = required %v properties %v, want required %v properties %v as its Go type encodes",
			gotRequired, gotProperties, wantRequired, wantProperties)
	}
}
