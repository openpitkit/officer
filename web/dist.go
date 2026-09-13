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

// Package web embeds the built operator dashboard so the single binary serves
// the SPA with no external assets. The Go embed directive cannot reference
// parent directories, so the //go:embed dist directive lives in this package,
// next to the dist directory it embeds.
package web

import (
	"embed"
	"fmt"
	"io/fs"
)

// distFS embeds the built single-page app produced by `npm run build` in
// officer/web. The repository commits a stable web/dist placeholder so this
// directive compiles before the real build runs; the Docker build overwrites
// web/dist with the compiled assets before the Go build.
//
//go:embed dist
var distFS embed.FS

// Dist returns the embedded SPA as a filesystem rooted at the dist directory,
// so its entries are "index.html", "assets/...", and so on - not
// "dist/index.html". The Officer composition installs it as the builder's SPA
// factory; the framework router serves static assets and the SPA fallback from
// it.
func Dist() (fs.FS, error) {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil, fmt.Errorf("web: sub dist: %w", err)
	}
	return sub, nil
}
