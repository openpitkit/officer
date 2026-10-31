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
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

const preMigrationTestName = "officer.db.pre-migration-20261028T120000Z"

type preMigrationContextKey struct{}

type fakePreMigrationBackups struct {
	copies    []PreMigrationBackup
	listErr   error
	openErr   error
	auditErr  error
	removeErr error
	file      *os.File
	body      string
	events    []string
	audits    []store.AuditEntry
	auditCtx  context.Context
}

func (f *fakePreMigrationBackups) router(t *testing.T) http.Handler {
	t.Helper()
	path := filepath.Join(t.TempDir(), "copy")
	if err := os.WriteFile(path, []byte(f.body), 0600); err != nil {
		t.Fatal(err)
	}
	source := PreMigrationBackupSource{
		List: func() ([]PreMigrationBackup, error) { return f.copies, f.listErr },
		Open: func(name string) (*os.File, int64, error) {
			f.events = append(f.events, "open:"+name)
			if f.openErr != nil {
				return nil, 0, f.openErr
			}
			var err error
			f.file, err = os.Open(path)
			return f.file, int64(len(f.body)), err
		},
		Audit: func(ctx context.Context, action domain.AuditAction, detail domain.AuditDetail) error {
			f.events = append(f.events, "audit")
			f.audits = append(f.audits, store.AuditEntry{Action: action, Detail: detail})
			f.auditCtx = ctx
			return f.auditErr
		},
		Remove: func(name string) error {
			f.events = append(f.events, "remove:"+name)
			assertPreMigrationFileClosed(t, f.file)
			return f.removeErr
		},
	}
	router := chi.NewRouter()
	for _, route := range PreMigrationBackupRoutes(source) {
		if route.DuringSetup {
			t.Fatalf("route %s must not answer during setup", route.ID)
		}
		router.Method(route.Method, route.Pattern, route.Handler)
	}
	return router
}

func assertPreMigrationFileClosed(t *testing.T, file *os.File) {
	t.Helper()
	if file == nil {
		t.Fatal("no file was opened")
	}
	if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("file.Stat after handling = %v, want closed file", err)
	}
}

func assertPreMigrationAudit(t *testing.T, f *fakePreMigrationBackups, action domain.AuditAction, code domain.AuditDetailCode, name string) {
	t.Helper()
	want := store.AuditEntry{Action: action,
		Detail: domain.NewAuditDetail(code, domain.AuditDetailParams{"name": name})}
	if len(f.audits) != 1 || !reflect.DeepEqual(f.audits[0], want) {
		t.Fatalf("audits = %+v, want exactly %+v", f.audits, want)
	}
	if f.auditCtx.Err() != nil || f.auditCtx.Done() != nil || f.auditCtx.Value(preMigrationContextKey{}) != "caller" {
		t.Fatalf("audit context lost values or retained cancellation: %v", f.auditCtx)
	}
}

func preMigrationRequest(method, name string) *http.Request {
	path := "/database/pre-migration-backups/" + url.PathEscape(name)
	if method == http.MethodPost {
		path += "/download"
	}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), preMigrationContextKey{}, "caller"))
	cancel()
	return httptest.NewRequest(method, path, nil).WithContext(ctx)
}

func TestPreMigrationBackupList(t *testing.T) {
	newest := time.Date(2026, 10, 28, 15, 0, 0, 0, time.FixedZone("offset", 3*60*60))
	f := &fakePreMigrationBackups{copies: []PreMigrationBackup{
		{Name: preMigrationTestName, CreatedAt: newest, SizeBytes: 23},
		{Name: "officer.db.pre-migration-20261027T120000Z", CreatedAt: newest.Add(-24 * time.Hour), SizeBytes: 17},
	}}
	rec := httptest.NewRecorder()
	f.router(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/database/pre-migration-backups", nil))
	want := `{"backups":[{"name":"officer.db.pre-migration-20261028T120000Z","createdAt":"2026-10-28T12:00:00Z","sizeBytes":23},{"name":"officer.db.pre-migration-20261027T120000Z","createdAt":"2026-10-27T12:00:00Z","sizeBytes":17}]}`
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != want {
		t.Fatalf("list = %d %s, want 200 %s", rec.Code, rec.Body, want)
	}
	if len(f.events) != 0 {
		t.Fatalf("list called mutation source: %v", f.events)
	}

	t.Run("empty", func(t *testing.T) {
		f := &fakePreMigrationBackups{}
		rec := httptest.NewRecorder()
		f.router(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/database/pre-migration-backups", nil))
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"backups":[]}` {
			t.Fatalf("empty list = %d %s", rec.Code, rec.Body)
		}
	})
	t.Run("unreadable directory", func(t *testing.T) {
		f := &fakePreMigrationBackups{listErr: errors.New("directory unreadable")}
		rec := httptest.NewRecorder()
		f.router(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/database/pre-migration-backups", nil))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("failed list = %d %s", rec.Code, rec.Body)
		}
	})
}

func TestPreMigrationBackupRefusesBeforeAudit(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		for _, tc := range []struct {
			name   string
			err    error
			status int
		}{
			{"invalid", domain.ErrInvalid, http.StatusUnprocessableEntity},
			{"missing", domain.ErrNotFound, http.StatusNotFound},
		} {
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				f := &fakePreMigrationBackups{openErr: tc.err}
				rec := httptest.NewRecorder()
				f.router(t).ServeHTTP(rec, preMigrationRequest(method, preMigrationTestName))
				if rec.Code != tc.status || !slices.Equal(f.events, []string{"open:" + preMigrationTestName}) || len(f.audits) != 0 {
					t.Fatalf("response = %d %s, events = %v, audits = %v", rec.Code, rec.Body, f.events, f.audits)
				}
			})
		}
	}
}

func TestPreMigrationBackupAuditFailure(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			f := &fakePreMigrationBackups{body: "private database bytes", auditErr: errors.New("audit unavailable")}
			rec := httptest.NewRecorder()
			f.router(t).ServeHTTP(rec, preMigrationRequest(method, preMigrationTestName))
			if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), f.body) {
				t.Fatalf("audit failure = %d %s", rec.Code, rec.Body)
			}
			if rec.Header().Get("Content-Disposition") != "" || rec.Header().Get("Content-Length") != "" {
				t.Fatalf("audit failure sent download headers: %v", rec.Header())
			}
			if !slices.Equal(f.events, []string{"open:" + preMigrationTestName, "audit"}) {
				t.Fatalf("audit failure events = %v", f.events)
			}
			assertPreMigrationFileClosed(t, f.file)
		})
	}
}

func TestPreMigrationBackupDownload(t *testing.T) {
	for _, name := range []string{preMigrationTestName, "officer \"; % café.db.pre-migration-20261028T120000Z"} {
		t.Run(name, func(t *testing.T) {
			f := &fakePreMigrationBackups{body: "SQLite format 3\x00\x01\xff\n"}
			rec := httptest.NewRecorder()
			f.router(t).ServeHTTP(rec, preMigrationRequest(http.MethodPost, name))
			if rec.Code != http.StatusOK || rec.Body.String() != f.body {
				t.Fatalf("download = %d %q", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("Content-Type") != "application/octet-stream" || rec.Header().Get("Content-Length") != strconv.Itoa(len(f.body)) {
				t.Fatalf("download headers = %v", rec.Header())
			}
			disposition, params, err := mime.ParseMediaType(rec.Header().Get("Content-Disposition"))
			if err != nil || disposition != "attachment" || params["filename"] != name {
				t.Fatalf("Content-Disposition = %q: %v", rec.Header().Get("Content-Disposition"), err)
			}
			assertPreMigrationFileClosed(t, f.file)
			assertPreMigrationAudit(t, f, domain.AuditActionDownloadPreMigrationBackup, domain.AuditDetailDownloadPreMigrationBackup, name)
			if !slices.Equal(f.events, []string{"open:" + name, "audit"}) {
				t.Fatalf("download events = %v", f.events)
			}
		})
	}
}

func TestPreMigrationBackupDelete(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"success", nil, http.StatusNoContent},
		{"removal failure", errors.New("disk failure"), http.StatusInternalServerError},
		{"removed concurrently", domain.ErrNotFound, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakePreMigrationBackups{removeErr: tc.err}
			rec := httptest.NewRecorder()
			f.router(t).ServeHTTP(rec, preMigrationRequest(http.MethodDelete, preMigrationTestName))
			if rec.Code != tc.status {
				t.Fatalf("delete = %d %s, want %d", rec.Code, rec.Body, tc.status)
			}
			if tc.err != nil {
				var response struct {
					Error struct{ Message string } `json:"error"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || !strings.Contains(response.Error.Message, preMigrationTestName) {
					t.Fatalf("removal error does not name the copy: %s (%v)", rec.Body, err)
				}
			} else if rec.Body.Len() != 0 {
				t.Fatalf("successful delete body = %q", rec.Body.String())
			}
			assertPreMigrationAudit(t, f, domain.AuditActionDeletePreMigrationBackup, domain.AuditDetailDeletePreMigrationBackup, preMigrationTestName)
			if !slices.Equal(f.events, []string{"open:" + preMigrationTestName, "audit", "remove:" + preMigrationTestName}) {
				t.Fatalf("delete events = %v", f.events)
			}
		})
	}
}
