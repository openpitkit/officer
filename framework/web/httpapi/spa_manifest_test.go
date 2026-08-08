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
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestSPAHandlerServesAppRouteManifestAsset(t *testing.T) {
	const manifest = `{"routes":[{"id":"dashboard","path":"/","kind":"canonical"}]}`
	handler, err := newSPAHandler(fstest.MapFS{
		"index.html": {Data: []byte("<html>shell</html>")},
		"app-route-manifest.json": {
			Data: []byte(manifest),
		},
	})
	if err != nil {
		t.Fatalf("newSPAHandler: %v", err)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "/app-route-manifest.json", nil),
	)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if got := recorder.Body.String(); got != manifest {
		t.Fatalf("body = %q, want manifest asset", got)
	}
}
