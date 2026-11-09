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
	"net/url"
	"reflect"
	"strconv"
	"testing"

	"go.openpit.dev/officer/framework/store"
)

func TestTextMatcherFromQuery(t *testing.T) {
	for _, tc := range []struct {
		name, value, mode string
		want              store.TextMatcher
		err               string
	}{
		{"empty", "", "unknown", store.TextMatcher{}, ""},
		{"default", "a*b", "", store.TextMatcher{Fragments: []string{"a", "b"}}, ""},
		{"contains", "a*b", "contains", store.TextMatcher{Fragments: []string{"a", "b"}}, ""},
		{"starts", "a*", "starts_with", store.TextMatcher{Fragments: []string{"a", ""}, AnchorStart: true}, ""},
		{"ends", "*b", "ends_with", store.TextMatcher{Fragments: []string{"", "b"}, AnchorEnd: true}, ""},
		{"exact", "a*b", "exact", store.TextMatcher{Fragments: []string{"a", "b"}, AnchorStart: true, AnchorEnd: true}, ""},
		{"bad mode", "a", "unknown", store.TextMatcher{}, "invalid codeMatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := TextMatcherFromQuery(url.Values{"code": {tc.value}, "codeMatch": {tc.mode}}, "code", "codeMatch")
			if tc.err != "" {
				if err == nil || err.Error() != tc.err {
					t.Fatalf("error = %v; want %s", err, tc.err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("matcher = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func TestPageSpecFromQuery(t *testing.T) {
	overflow := strconv.FormatUint(^uint64(0), 10)
	maxInt := strconv.Itoa(int(^uint(0) >> 1))
	for _, tc := range []struct {
		query string
		want  store.PageSpec
		err   string
	}{
		{"", store.PageSpec{Limit: 100}, ""},
		{"limit=2000", store.PageSpec{Limit: 1000}, ""},
		{"limit=25&page=3", store.PageSpec{Limit: 25, Offset: 50}, ""},
		{"limit=25&page=bad&offset=7", store.PageSpec{Limit: 25, Offset: 7}, ""},
		{"page=1", store.PageSpec{Limit: 100}, ""},
		{"offset=0&page=0", store.PageSpec{Limit: 100}, ""},
		{"limit=0", store.PageSpec{}, "invalid limit"},
		{"limit=-1", store.PageSpec{}, "invalid limit"},
		{"limit=bad", store.PageSpec{}, "invalid limit"},
		{"limit=" + overflow, store.PageSpec{}, "invalid limit"},
		{"offset=-1", store.PageSpec{}, "invalid offset"},
		{"offset=bad", store.PageSpec{}, "invalid offset"},
		{"page=0", store.PageSpec{}, "invalid page"},
		{"page=-1", store.PageSpec{}, "invalid page"},
		{"page=bad", store.PageSpec{}, "invalid page"},
		{"page=" + overflow, store.PageSpec{}, "invalid page"},
		{"limit=1000&page=" + maxInt, store.PageSpec{}, "invalid page"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			values, err := url.ParseQuery(tc.query)
			if err != nil {
				t.Fatal(err)
			}
			got, err := PageSpecFromQuery(values)
			if tc.err != "" {
				if err == nil || err.Error() != tc.err {
					t.Fatalf("error = %v; want %s", err, tc.err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("page = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}
