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

// Package httpapi provides the framework HTTP router and route-registry seam.
package httpapi

import "net/http"

// Route is a single REST route registered as data.
type Route struct {
	ID      string
	Method  string
	Pattern string
	Handler http.Handler
}

// RouteRegistry collects v1 routes in insertion order, keyed by stable route id.
type RouteRegistry struct {
	routes []Route
	index  map[string]int
}

// Register adds or replaces a route. Re-registering the same id replaces the
// route in place so route order remains stable.
func (r *RouteRegistry) Register(route Route) {
	if route.ID == "" {
		panic("httpapi: route id is empty")
	}
	if r.index == nil {
		r.index = make(map[string]int)
	}
	if i, ok := r.index[route.ID]; ok {
		r.routes[i] = route
		return
	}
	r.index[route.ID] = len(r.routes)
	r.routes = append(r.routes, route)
}

// Unregister removes a route by id and reports whether it existed.
func (r *RouteRegistry) Unregister(id string) bool {
	if r == nil || r.index == nil {
		return false
	}
	i, ok := r.index[id]
	if !ok {
		return false
	}
	delete(r.index, id)
	r.routes = append(r.routes[:i], r.routes[i+1:]...)
	for pos := i; pos < len(r.routes); pos++ {
		r.index[r.routes[pos].ID] = pos
	}
	return true
}

// Routes returns registered routes in insertion order.
func (r *RouteRegistry) Routes() []Route {
	if r == nil {
		return nil
	}
	out := make([]Route, len(r.routes))
	copy(out, r.routes)
	return out
}

// Middleware is one HTTP middleware.
type Middleware = func(http.Handler) http.Handler

// Chain is an ordered middleware list.
type Chain []Middleware

func (c Chain) Then(h http.Handler) http.Handler {
	for i := len(c) - 1; i >= 0; i-- {
		h = c[i](h)
	}
	return h
}
