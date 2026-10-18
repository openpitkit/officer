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
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"go.openpit.dev/officer/framework/domain"
)

// BrowserGuards are the checks that refuse a state-changing request a page
// from another site, open in the operator's browser, sends as a form or a
// script, and a request to a domain re-bound to the server's address.
// NewRouter applies them to every request, in field order, before routing,
// authentication, or body parsing. A host that composes its own router
// instead of NewRouter applies them itself, in the same order.
type BrowserGuards struct {
	// Host refuses a request addressed to a host name the server does not
	// answer to, as a page on a domain re-bound to the server's address is.
	Host Middleware
	// Origin refuses a state-changing request a browser sent from another
	// origin.
	Origin Middleware
	// ContentType refuses a state-changing request whose body is not declared
	// as JSON: a page sends a cross-site body without a CORS preflight only as
	// a form or as text/plain.
	ContentType Middleware
}

// dnsNamePattern matches the ASCII DNS name syntax AllowedHosts requires.
var dnsNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)*$`)

// numericLastLabelPattern matches a name whose last label is a number, decimal
// or hexadecimal after 0x, which a browser parses as an IPv4 address.
var numericLastLabelPattern = regexp.MustCompile(`(^|\.)([0-9]+|0[xX][0-9A-Fa-f]*)$`)

// AllowedHosts refuses, with domain.ErrHostNotAllowed, a request whose host is
// not one of names. The host is the request's Host without its port and IPv6
// brackets, compared with names case-insensitively and exactly, an IP address
// on either side in its canonical form: no wildcard, no suffix match, no
// trailing-dot folding. The port is never checked, and a
// request without a host is refused. Each name must be a bare IP address that
// net.ParseIP accepts, with no brackets and no zone, or an ASCII DNS name: one
// or more labels of letters, digits, '-' and '_', separated by single dots, the
// last of which is not a number a browser would read as an IPv4 address.
// AllowedHosts returns an error for an empty list and for any other name.
func AllowedHosts(names []string) (Middleware, error) {
	if len(names) == 0 {
		return nil, errors.New("httpapi: no allowed hosts")
	}
	allowed := make([]string, 0, len(names))
	for _, name := range names {
		if net.ParseIP(name) == nil &&
			(!dnsNamePattern.MatchString(name) || numericLastLabelPattern.MatchString(name)) {
			return nil, fmt.Errorf(
				"httpapi: allowed host %q is neither an IP address nor an ASCII DNS name",
				name,
			)
		}
		allowed = append(allowed, canonicalHost(name))
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Hostname drops a numeric port and the IPv6 brackets.
			host := canonicalHost((&url.URL{Host: r.Host}).Hostname())
			if !slices.Contains(allowed, host) {
				WriteErr(w, domain.ErrHostNotAllowed)
				return
			}
			next.ServeHTTP(w, r)
		})
	}, nil
}

// canonicalHost returns host in the form AllowedHosts compares: an IP address
// in its canonical text, any other name in lower case.
func canonicalHost(host string) string {
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return strings.ToLower(host)
}

// SameOrigin refuses, with domain.ErrOriginNotAllowed, a state-changing
// request a browser sent from another origin. It is the standard library's
// http.CrossOriginProtection: GET, HEAD and OPTIONS pass, and so does a
// request that carries neither Sec-Fetch-Site nor Origin, as a request from a
// program does.
func SameOrigin() Middleware {
	protection := http.NewCrossOriginProtection()
	protection.SetDenyHandler(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			WriteErr(w, domain.ErrOriginNotAllowed)
		},
	))
	return protection.Handler
}

// sameOriginOnEveryMethod refuses, with domain.ErrOriginNotAllowed, a request a
// browser sent from another origin, whatever its method: NewRouter puts it in
// front of MCP over HTTP, where a GET opens an event stream and an MCP session
// that a page from another site can open with EventSource, without a
// preflight. It is SameOrigin's rule on every method. Check passes GET, HEAD
// and OPTIONS before looking further, so it judges a copy of the request whose
// method is POST, and the request itself reaches next unchanged; past that
// switch, Check reads the method only to match a bypass pattern, and this
// protection has none.
func sameOriginOnEveryMethod() Middleware {
	protection := http.NewCrossOriginProtection()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			asWrite := *r
			asWrite.Method = http.MethodPost
			if protection.Check(&asWrite) != nil {
				WriteErr(w, domain.ErrOriginNotAllowed)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// JSONBody refuses, with domain.ErrUnsupportedMediaType, a request other than
// GET, HEAD or OPTIONS whose body is not declared as JSON: a request with a
// non-zero or unknown Content-Length must carry exactly one Content-Type
// value, application/json, with no parameter other than charset=utf-8. It
// refuses before anything reads the body. A request whose Content-Length is
// zero passes.
func JSONBody() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			safe := r.Method == http.MethodGet ||
				r.Method == http.MethodHead ||
				r.Method == http.MethodOptions
			if !safe && r.ContentLength != 0 &&
				!isJSONMediaType(r.Header.Values("Content-Type")) {
				WriteErr(w, domain.ErrUnsupportedMediaType)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// isJSONMediaType reports whether values is a single application/json media
// type with no parameter other than charset=utf-8. mime.ParseMediaType drops
// a malformed RFC 2231 parameter and a trailing ';', and merges a parameter
// repeated with the same value, all without an error, so the raw value must
// also hold exactly one ';'-separated segment per parsed parameter, each
// named charset.
func isJSONMediaType(values []string) bool {
	if len(values) != 1 {
		return false
	}
	mediaType, params, err := mime.ParseMediaType(values[0])
	if err != nil || mediaType != "application/json" {
		return false
	}
	segments := strings.Split(values[0], ";")[1:]
	if len(segments) != len(params) {
		return false
	}
	for _, segment := range segments {
		name, _, _ := strings.Cut(segment, "=")
		if !strings.EqualFold(strings.TrimSpace(name), "charset") {
			return false
		}
	}
	for name, value := range params {
		if name != "charset" || !strings.EqualFold(value, "utf-8") {
			return false
		}
	}
	return true
}
