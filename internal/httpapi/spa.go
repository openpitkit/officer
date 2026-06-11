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
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// indexFile is the SPA shell filename expected at the root of the dist filesystem.
const indexFile = "index.html"

// spaHandler serves a single-page application from an embedded filesystem. It
// serves a real asset when the requested path names one, and otherwise falls
// back to index.html so the client-side router can resolve the route. This is
// the standard SPA-hosting pattern: deep links and refreshes on client routes
// return the app shell instead of a 404.
type spaHandler struct {
	files     fs.FS
	fileSrv   http.Handler
	indexPage []byte
}

// newSPAHandler builds an spaHandler over the given dist filesystem. The
// filesystem must contain index.html at its root; newSPAHandler reads it once
// so the fallback never re-opens the file per request. It returns an error if
// index.html is missing or unreadable.
func newSPAHandler(files fs.FS) (*spaHandler, error) {
	index, err := fs.ReadFile(files, indexFile)
	if err != nil {
		return nil, fmt.Errorf("httpapi: read %s: %w", indexFile, err)
	}
	return &spaHandler{
		files:     files,
		fileSrv:   http.FileServer(http.FS(files)),
		indexPage: index,
	}, nil
}

// ServeHTTP serves the requested asset, or the SPA shell as a fallback. A
// request for an existing file (for example /assets/app.js) is served by the
// embedded file server; anything else - the root, or a client-side route like
// /accounts - returns index.html with a 200 so the SPA can route it.
func (h *spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := assetName(r.URL.Path)
	if name != "" && h.assetExists(name) {
		h.fileSrv.ServeHTTP(w, r)
		return
	}
	h.serveIndex(w)
}

// serveIndex writes the cached SPA shell.
func (h *spaHandler) serveIndex(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(h.indexPage)
}

// assetExists reports whether name resolves to a regular file in the embedded
// filesystem. Directories are not assets: a request for a directory falls back
// to the SPA shell.
func (h *spaHandler) assetExists(name string) bool {
	info, err := fs.Stat(h.files, name)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// assetName maps a request path to an io/fs lookup name (slash-rooted, no
// leading slash, cleaned). It returns "" for the application root, which always
// falls back to the SPA shell. A cleaned path can never escape the embedded
// filesystem, so traversal attempts resolve harmlessly inside dist.
func assetName(urlPath string) string {
	cleaned := path.Clean("/" + strings.TrimPrefix(urlPath, "/"))
	trimmed := strings.TrimPrefix(cleaned, "/")
	if trimmed == "" || trimmed == "." {
		return ""
	}
	return trimmed
}
