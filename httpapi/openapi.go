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
	_ "embed"
	"net/http"
)

//go:embed openapi.yaml
var openapiSpec []byte

//go:embed swaggerui/swagger-ui.css
var swaggerUICSS []byte

//go:embed swaggerui/swagger-ui-bundle.js
var swaggerUIBundle []byte

//go:embed swagger-initializer.js
var swaggerUIInitializer []byte

//go:embed swaggerui/swagger-ui-bundle.js.LICENSE.txt
var swaggerUIBundleLicense []byte

//go:embed swaggerui/LICENSE
var swaggerUILicense []byte

//go:embed swaggerui/NOTICE
var swaggerUINotice []byte

// serveSwaggerUI serves the /docs page, Swagger UI for /api/openapi.yaml. The
// page loads the vendored swagger-ui-dist files and Officer's initializer from
// the /docs routes beside it, and its content security policy admits only
// same-origin scripts, styles and requests. `just vendor-swagger-ui` refreshes
// the vendored files.
func serveSwaggerUI(w http.ResponseWriter, _ *http.Request) {
	const html = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8"/>
  <meta name="viewport" content="width=device-width, initial-scale=1"/>
  <title>Pit Officer API</title>
  <link rel="stylesheet" href="/docs/swagger-ui.css"/>
</head>
<body>
<div id="swagger-ui"></div>
<script src="/docs/swagger-ui-bundle.js"></script>
<script src="/docs/swagger-initializer.js"></script>
</body>
</html>
`
	// The data: images are the icons swagger-ui.css inlines.
	const policy = "default-src 'none'; script-src 'self'; style-src 'self'; " +
		"img-src 'self' data:; connect-src 'self'; font-src 'self'; " +
		"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", policy)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(html))
}

// serveEmbeddedFile serves an embedded file's bytes as contentType, with MIME
// sniffing off, so the browser uses them only as that type.
func serveEmbeddedFile(contentType string, body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}
}
