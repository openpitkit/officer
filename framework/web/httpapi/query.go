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
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"go.openpit.dev/officer/framework/store"
)

// DefaultListLimit is the REST list page size when no limit is supplied.
const DefaultListLimit = 100

// MaxListLimit caps an explicit REST list limit.
const MaxListLimit = 1000

// TextMatcherFromQuery parses a wildcard text filter and its match mode.
// Empty values leave the filter unset; unknown modes return an error.
func TextMatcherFromQuery(q url.Values, valueKey, modeKey string) (
	store.TextMatcher, error,
) {
	value := q.Get(valueKey)
	if value == "" {
		return store.TextMatcher{}, nil
	}
	mode := q.Get(modeKey)
	matcher := store.TextMatcher{Fragments: strings.Split(value, "*")}
	switch mode {
	case "", "contains":
	case "starts_with":
		matcher.AnchorStart = true
	case "ends_with":
		matcher.AnchorEnd = true
	case "exact":
		matcher.AnchorStart = true
		matcher.AnchorEnd = true
	default:
		return store.TextMatcher{}, fmt.Errorf("invalid %s", modeKey)
	}
	return matcher, nil
}

// PageSpecFromQuery parses REST pagination with a default limit of 100 and
// a cap of 1000. An explicit offset takes precedence over the one-based page.
func PageSpecFromQuery(q url.Values) (store.PageSpec, error) {
	limit := DefaultListLimit
	if raw := q.Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			return store.PageSpec{}, fmt.Errorf("invalid limit")
		}
		if value > MaxListLimit {
			value = MaxListLimit
		}
		limit = value
	}
	offset := 0
	if raw := q.Get("offset"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			return store.PageSpec{}, fmt.Errorf("invalid offset")
		}
		offset = value
	} else if raw := q.Get("page"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			return store.PageSpec{}, fmt.Errorf("invalid page")
		}
		page, err := store.PageAt(limit, value)
		if err != nil {
			return store.PageSpec{}, fmt.Errorf("invalid page")
		}
		offset = page.Offset
	}
	return store.PageSpec{Limit: limit, Offset: offset}, nil
}
