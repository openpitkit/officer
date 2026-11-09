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
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"

	"go.openpit.dev/officer/framework/store"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

// Source: openapi.yaml - GET list text-filter parameter descriptions.
func TestOpenAPIListTextBoundsContract(t *testing.T) {
	t.Parallel()
	sentences := map[string]string{
		"search": fmt.Sprintf("The value holds at most %d `*`-separated fragments and at most %d Unicode code points across them, the `*` separators not counted; a longer value, or one containing NUL, is refused with 422 `validation`.",
			store.TextMatcherMaxFragments, store.TextMatcherMaxRunes),
		"exact": fmt.Sprintf("The value holds at most %d Unicode code points; a longer value, or one containing NUL, is refused with 422 `validation`.",
			store.TextMatcherMaxRunes),
	}
	for _, tc := range []struct {
		list      boundsList
		parameter string
		kind      string
		bounded   bool
	}{
		{boundsAccounts, "code", "search", true},
		{boundsAccounts, "blockReason", "search", true},
		{boundsGroups, "code", "search", true},
		{boundsGroups, "notes", "search", true},
		{boundsGroups, "blockReason", "search", true},
		{boundsOrders, "baseAsset", "exact", true},
		{boundsOrders, "quoteAsset", "exact", true},
		{boundsTrades, "account", "exact", true},
		{boundsTrades, "baseAsset", "exact", true},
		{boundsTrades, "quoteAsset", "exact", true},
		// These lists do not bound their text filters today; bounding one
		// must also document it on that list's parameter.
		{boundsList{path: "/api/v1/assets"}, "code", "search", false},
		{boundsList{path: "/api/v1/assets"}, "class", "search", false},
		{boundsList{path: "/api/v1/asset-classes"}, "code", "search", false},
		{boundsList{path: "/api/v1/asset-classes"}, "notes", "search", false},
		{boundsList{path: "/api/v1/audit"}, "actor", "search", false},
	} {
		t.Run(tc.list.path+"/"+tc.parameter, func(t *testing.T) {
			t.Parallel()
			parameter := openAPIListParameter(t, strings.TrimPrefix(tc.list.path, "/api/v1"), tc.parameter)
			description := openAPIParameterField(t, parameter, "description")
			for kind, sentence := range sentences {
				want := tc.bounded && tc.kind == kind
				if got := strings.Contains(description, sentence); got != want {
					t.Errorf("%s bounds sentence present = %t, want %t", kind, got, want)
				}
			}
			status := http.StatusOK
			if tc.bounded {
				status = http.StatusUnprocessableEntity
			}
			type textProbe struct {
				value string
				want  int
			}
			values := map[string]textProbe{"runes": {tooManyRunes, status}}
			if tc.bounded {
				values["NUL"] = textProbe{"a\x00b", status}
				if tc.kind == "search" {
					values["fragments"] = textProbe{fragments17, status}
					values["separators_not_counted"] = textProbe{
						strings.Repeat("界", store.TextMatcherMaxRunes/2) + "*" +
							strings.Repeat("界", store.TextMatcherMaxRunes-store.TextMatcherMaxRunes/2), http.StatusOK}
				}
			}
			for name, probe := range values {
				t.Run(name, func(t *testing.T) {
					rec, _ := serveWithin(t, tc.list, url.Values{tc.parameter: {probe.value}}.Encode())
					if rec.Code != probe.want {
						t.Fatalf("status = %d, want %d: %s", rec.Code, probe.want, rec.Body.String())
					}
					if probe.want == http.StatusUnprocessableEntity {
						validationProblem(t, rec.Body.Bytes())
					}
				})
			}
		})
	}
}

// Source: openapi.yaml - PositionCount* and AccountCount* parameters.
func TestOpenAPIListCountBoundsContract(t *testing.T) {
	t.Parallel()
	identifiers := func(value string) []string {
		return strings.FieldsFunc(value, func(r rune) bool {
			return r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})
	}
	const betweenSentence = "`between` with the lower bound above the upper bound is refused with 422 `validation`."
	for _, tc := range []struct {
		list   boundsList
		prefix string
	}{
		{boundsAccounts, "positionCount"},
		{boundsGroups, "positionCount"},
		{boundsGroups, "accountCount"},
	} {
		t.Run(tc.list.path+"/"+tc.prefix, func(t *testing.T) {
			t.Parallel()
			path := strings.TrimPrefix(tc.list.path, "/api/v1")
			modeKey := tc.prefix + "Mode"
			mode := openAPIListParameter(t, path, modeKey)
			modes := identifiers(openAPIParameterField(t, mode, "enum"))
			if len(modes) == 0 {
				t.Fatal("mode enum is empty")
			}
			for _, bound := range []struct{ missing, present string }{{"Min", "Max"}, {"Max", "Min"}} {
				key := tc.prefix + bound.missing
				parameter := openAPIListParameter(t, path, key)
				description := openAPIParameterField(t, parameter, "description")
				_, required, found := strings.Cut(description, "Required for ")
				if !found {
					t.Fatalf("%s description has no Required for list", key)
				}
				required, _, found = strings.Cut(required, ";")
				if !found {
					t.Fatalf("%s Required for list has no terminating semicolon", key)
				}
				for _, mode := range modes {
					if mode == "all" {
						continue
					}
					t.Run(bound.missing+"/"+mode, func(t *testing.T) {
						needsBound := slices.Contains(identifiers(required), mode)
						for _, probe := range []struct{ name, value string }{
							{"missing", ""}, {"invalid", "x"},
						} {
							t.Run(probe.name, func(t *testing.T) {
								query := url.Values{modeKey: {mode}, tc.prefix + bound.present: {"1"}, key: {probe.value}}
								rec, _ := serveWithin(t, tc.list, query.Encode())
								want := http.StatusOK
								if needsBound {
									want = http.StatusUnprocessableEntity
								}
								if rec.Code != want {
									t.Fatalf("status = %d, want %d: %s", rec.Code, want, rec.Body.String())
								}
								if needsBound {
									problem := validationProblem(t, rec.Body.Bytes())
									detail := probe.name + " " + key
									if problem.Detail != detail {
										t.Fatalf("problem = %+v, want validation with detail %q", problem, detail)
									}
								}
							})
						}
					})
				}
			}
			if !strings.Contains(openAPIParameterField(t, mode, "description"), betweenSentence) {
				t.Errorf("%s description is missing the between ordering sentence", modeKey)
			}
			query := url.Values{modeKey: {"between"}, tc.prefix + "Min": {"2"}, tc.prefix + "Max": {"1"}}
			rec, _ := serveWithin(t, tc.list, query.Encode())
			problem := validationProblem(t, rec.Body.Bytes())
			if rec.Code != http.StatusUnprocessableEntity || problem.Detail != "invalid "+modeKey+" range" {
				t.Fatalf("status = %d, problem = %+v, want validation with detail %q",
					rec.Code, problem, "invalid "+modeKey+" range")
			}
		})
	}
}

// Source: openapi.yaml - GET /groups account-count parameters.
func TestOpenAPIGroupAccountCountContract(t *testing.T) {
	t.Parallel()
	openAPIListParameter(t, "/groups", "accountCountMode")
	if _, found := findOpenAPIListParameter(t, "/groups", "accountCount"); found {
		t.Fatal("GET /groups declares the ignored accountCount parameter")
	}
	baseline, baselineSvc := serveWithin(t, boundsGroups, "")
	if baseline.Code != http.StatusOK || !boundsGroups.read(baselineSvc) {
		t.Fatalf("baseline status = %d, filter = %+v, want 200 and a service read",
			baseline.Code, baselineSvc.groupFilter)
	}
	// By owner decision accountCount is ignored, not rejected.
	for _, value := range []string{"has", "none"} {
		t.Run(value, func(t *testing.T) {
			rec, svc := serveWithin(t, boundsGroups, "accountCount="+value)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			if !reflect.DeepEqual(svc.groupFilter, baselineSvc.groupFilter) {
				t.Fatalf("group filter = %+v, want unchanged %+v", svc.groupFilter, baselineSvc.groupFilter)
			}
		})
	}
}

// Source: openapi.yaml - LimitParam. Runtime coverage extends the existing
// TestListOrders_BadLimit, TestListOrders_LimitCapping and
// TestListAccountAdjustments_BadLimit tests.
func TestOpenAPIListLimitContract(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/orders", "/accounts/{code}/adjustments"} {
		t.Run(path, func(t *testing.T) {
			parameter := openAPIListParameter(t, path, "limit")
			if got := openAPIParameterField(t, parameter, "default"); got != fmt.Sprint(httpx.DefaultListLimit) {
				t.Errorf("LimitParam default = %s, want %d", got, httpx.DefaultListLimit)
			}
			if got := openAPIParameterField(t, parameter, "minimum"); got != "1" {
				t.Errorf("LimitParam minimum = %s, want 1", got)
			}
			if got := openAPIParameterField(t, parameter, "format"); got != "int64" {
				t.Errorf("LimitParam format = %s, want int64", got)
			}
			for _, line := range strings.Split(parameter, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "maximum:") {
					t.Error("LimitParam must not declare maximum: larger values are accepted and capped")
				}
			}
			description := openAPIParameterField(t, parameter, "description")
			for _, clause := range []string{
				fmt.Sprintf("default %d", httpx.DefaultListLimit),
				fmt.Sprintf("A larger value within the 64-bit integer range is accepted and capped at %d", httpx.MaxListLimit),
				"zero, a negative value, a non-integer or a value beyond the 64-bit integer range is refused with 422 `validation`",
			} {
				if !strings.Contains(description, clause) {
					t.Errorf("LimitParam description is missing %q", clause)
				}
			}
		})
	}
}

func validationProblem(t *testing.T, body []byte) httpx.ProblemDetails {
	t.Helper()
	var problem httpx.ProblemDetails
	if err := json.Unmarshal(body, &problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if len(problem.Errors) != 1 || problem.Errors[0].Code != "validation" {
		t.Fatalf("problem = %+v, want validation", problem)
	}
	return problem
}

// openAPIListParameter resolves a named parameter declared by a GET operation.
func openAPIListParameter(t *testing.T, path, name string) string {
	t.Helper()
	block, found := findOpenAPIListParameter(t, path, name)
	if !found {
		t.Fatalf("GET %s has no parameter %s", path, name)
	}
	return block
}

func findOpenAPIListParameter(t *testing.T, path, name string) (string, bool) {
	t.Helper()
	components, found := openAPIComponents(strings.Split(string(openapiSpec), "\n"), "parameters")
	if !found {
		t.Fatal("no components/parameters section")
	}
	for _, operation := range openAPIOperations(string(openapiSpec)) {
		if operation.Method != http.MethodGet || operation.Path != path {
			continue
		}
		for i, line := range operation.Lines {
			if line != "      parameters:" {
				continue
			}
			for _, block := range childBlocks(nestedLines(operation.Lines, i), 8) {
				first, _, _ := strings.Cut(block, "\n")
				first = strings.TrimSpace(first)
				if first == "- name: "+name {
					return block, true
				}
				if !strings.HasPrefix(first, "- $ref: ") {
					continue
				}
				parameter, missing := resolveComponentRef(block, components, "parameters")
				if missing != "" {
					t.Fatalf("GET %s references missing components/parameters/%s", path, missing)
				}
				if openAPIParameterField(t, parameter, "name") == name {
					return parameter, true
				}
			}
		}
	}
	return "", false
}

// openAPIParameterField reads an inline or folded field from a parameter block.
func openAPIParameterField(t *testing.T, block, field string) string {
	t.Helper()
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		line = strings.TrimPrefix(strings.TrimSpace(line), "- ")
		value, found := strings.CutPrefix(line, field+": ")
		if !found {
			continue
		}
		if value == ">" || value == ">-" {
			value = strings.Join(nestedLines(lines, i), "\n")
		}
		return strings.Join(strings.Fields(value), " ")
	}
	t.Fatalf("parameter has no %s field", field)
	return ""
}

// The order, trade, account and group lists hold their filters to the core
// list-filter bounds through the filter's Validate: a value outside them
// answers 422 validation before the service reads the list, and a value at
// them reads as before.

const engineDecimalMax = "79228162514264337593543950335"

var (
	// mostRunes is a text filter at store.TextMatcherMaxRunes, and
	// tooManyRunes one code point past it.
	mostRunes    = strings.Repeat("x", store.TextMatcherMaxRunes)
	tooManyRunes = strings.Repeat("x", store.TextMatcherMaxRunes+1)
	// fragments16 is 16 fragments of 16 runes, at the fragment bound.
	fragments16 = strings.Repeat("abcdefghijklmnop*", 15) + "abcdefghijklmnop"
	fragments17 = strings.Repeat("a*", 16) + "a"
	// decimal65 is the value 1 written in 65 characters, one past the limit.
	decimal65 = "1." + strings.Repeat("0", 63)
)

// tooManyRunesDetail is the problem detail for field holding tooManyRunes.
func tooManyRunesDetail(field string) string {
	return fmt.Sprintf(
		"%s: %d code points, at most %d: invalid",
		field, store.TextMatcherMaxRunes+1, store.TextMatcherMaxRunes,
	)
}

// boundsList is one REST list and the probe that reports whether the fake
// service read it.
type boundsList struct {
	path string
	read func(svc *fakeService) bool
}

var (
	boundsOrders = boundsList{"/api/v1/orders", func(svc *fakeService) bool {
		return svc.orderListCalls != 0
	}}
	boundsTrades = boundsList{"/api/v1/trades", func(svc *fakeService) bool {
		return svc.tradeListCalls != 0
	}}
	// The account and group reads record their filter, which always carries
	// the default page limit, so a zero filter means the read never ran.
	boundsAccounts = boundsList{"/api/v1/accounts", func(svc *fakeService) bool {
		return !reflect.DeepEqual(svc.accountFilter, store.AccountListFilter{})
	}}
	boundsGroups = boundsList{"/api/v1/groups", func(svc *fakeService) bool {
		return !reflect.DeepEqual(svc.groupFilter, store.GroupListFilter{})
	}}
)

// serveWithin answers one GET of list.path?query against a fresh fake service
// and fails t unless the answer comes within a second, so a bound that sizes
// work by its exponent fails instead of stalling the suite.
func serveWithin(t *testing.T, list boundsList, query string) (*httptest.ResponseRecorder, *fakeService) {
	t.Helper()
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := serveWithinASecond(t, r, httptest.NewRequest(http.MethodGet, list.path+"?"+query, nil))
	return rec, svc
}

// serveWithinASecond answers req on h and fails t unless the answer comes
// within a second, so a value that sizes work by its exponent fails the test
// instead of stalling the suite.
func serveWithinASecond(t *testing.T, h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(rec, req)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("%s %.80s did not answer within a second", req.Method, req.URL.RequestURI())
	}
	return rec
}

func TestListBoundsRejectBeforeRead(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		list    boundsList
		query   string
		detail  string
		pointer string
	}{
		{
			name: "orders/amount", list: boundsOrders,
			query:  "amountMode=gte&amountMin=1e2147483647",
			detail: `amount.min: "1e2147483647" is outside the engine decimal range: invalid`,
		},
		{
			name: "orders/price", list: boundsOrders,
			query:  "priceMode=lt&priceMax=1e2147483647",
			detail: `price.max: "1e2147483647" is outside the engine decimal range: invalid`,
		},
		{
			name: "orders/amount-between", list: boundsOrders,
			query:  "amountMode=between&amountMin=-1e2147483647&amountMax=0e2147483647",
			detail: `amount.min: "-1e2147483647" is outside the engine decimal range: invalid`,
		},
		{
			name: "orders/amount-zero-exponent", list: boundsOrders,
			query:  "amountMode=gte&amountMin=0e2147483647",
			detail: `amount.min: "0e2147483647" has written exponent 2147483647, outside [-92, 28]: invalid`,
		},
		{
			name: "orders/base-asset-runes", list: boundsOrders,
			query:  "baseAsset=" + tooManyRunes,
			detail: tooManyRunesDetail("baseAsset"),
		},
		{
			name: "orders/repeated-status", list: boundsOrders,
			query:  "status=filled,cancelled&status=filled",
			detail: `status: "filled" is repeated: invalid`,
		},
		{
			name: "trades/quantity", list: boundsTrades,
			query:  "quantityMode=gte&quantityMin=1e2147483647",
			detail: `quantity.min: "1e2147483647" is outside the engine decimal range: invalid`,
		},
		{
			name: "trades/price", list: boundsTrades,
			query:  "priceMode=eq&priceMin=1e2147483647",
			detail: `price.equal: "1e2147483647" is outside the engine decimal range: invalid`,
		},
		{
			name: "trades/lock-price", list: boundsTrades,
			query:  "lockPriceMode=gte&lockPriceMin=1e2147483647",
			detail: `lockPrice.min: "1e2147483647" is outside the engine decimal range: invalid`,
		},
		{
			name: "trades/lock-price-between", list: boundsTrades,
			query:  "lockPriceMode=between&lockPriceMin=1&lockPriceMax=1e2147483647",
			detail: `lockPrice.max: "1e2147483647" is outside the engine decimal range: invalid`,
		},
		{
			name: "trades/quantity-length", list: boundsTrades,
			query:  "quantityMode=lte&quantityMax=" + decimal65,
			detail: "quantity.max: 65 code points, at most 64: invalid",
		},
		{
			name: "trades/account-runes", list: boundsTrades,
			query:  "account=" + tooManyRunes,
			detail: tooManyRunesDetail("account"),
		},
		{
			name: "accounts/pnl", list: boundsAccounts,
			query:  "pnlMode=gte&pnlMin=1e2147483647&pnlCurrency=USD",
			detail: `pnl.min: "1e2147483647" is outside the engine decimal range: invalid`,
		},
		{
			name: "accounts/pnl-between", list: boundsAccounts,
			query:  "pnlMode=between&pnlMin=1e-2147483648&pnlMax=1e2147483647&pnlCurrency=USD",
			detail: `pnl.min: "1e-2147483648" is outside the engine decimal range: invalid`,
		},
		{
			name: "accounts/code-fragments", list: boundsAccounts,
			query:  "code=" + fragments17,
			detail: "code: 17 fragments, at most 16: invalid",
		},
		{
			name: "accounts/block-reason-runes", list: boundsAccounts,
			query:  "blockReason=" + tooManyRunes + "&blockReasonMatch=exact",
			detail: tooManyRunesDetail("blockReason"),
		},
		{
			// The currency rule still comes first and keeps its pointer.
			name: "accounts/currency-before-text", list: boundsAccounts,
			query:   "includeUnsetPnl=true&code=" + fragments17,
			detail:  "pnl: currency is required to compare this value: invalid",
			pointer: "/pnlCurrency",
		},
		{
			name: "groups/code-fragments", list: boundsGroups,
			query:  "code=" + fragments17 + "&codeMatch=starts_with",
			detail: "code: 17 fragments, at most 16: invalid",
		},
		{
			name: "groups/notes-runes", list: boundsGroups,
			query:  "notes=" + tooManyRunes,
			detail: tooManyRunesDetail("notes"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec, svc := serveWithin(t, tc.list, tc.query)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
			}
			problem := validationProblem(t, rec.Body.Bytes())
			if problem.Detail != tc.detail || problem.Errors[0].Pointer != tc.pointer {
				t.Fatalf("problem = %+v, want validation at %q with detail %q",
					problem, tc.pointer, tc.detail)
			}
			if tc.list.read(svc) {
				t.Fatal("the service read the list after a rejected filter")
			}
		})
	}
}

// TestListBoundsBetweenOrder pins REST's own between check, which orders the
// two bounds by sign and leading digit before it compares them, to the
// numeric order on every branch. It runs while REST parses the query, before
// the core Validate, so every inverted pair answers REST's range error within
// the time budget, in the engine range or not.
func TestListBoundsBetweenOrder(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		min, max string
		inverted bool
	}{
		{min: "1", max: "2"},
		{min: "2", max: "1", inverted: true},
		{min: "-1", max: "1"},
		{min: "1", max: "-1", inverted: true},
		{min: "-100", max: "1"},
		{min: "1", max: "-100", inverted: true},
		{min: "-1", max: "0"},
		{min: "0", max: "-1", inverted: true},
		{min: "0", max: "0e5"},
		{min: "0e5", max: "-0"},
		{min: "-10", max: "-5"},
		{min: "-5", max: "-10", inverted: true},
		{min: "9.5", max: "10"},
		{min: "10", max: "9.5", inverted: true},
		{min: "1e2", max: "100"},
		{min: "1.10", max: "1.1"},
		{min: "1.2", max: "1.5"},
		{min: "1.5", max: "1.2", inverted: true},
		{min: "-1.2", max: "-1.5", inverted: true},
		// Inverted pairs with a bound the core refuses: REST orders them
		// first, without building ten to the gap between their exponents.
		{min: "1e2147483647", max: "1e-2147483648", inverted: true},
		{min: "1e29", max: "1", inverted: true},
		{min: "1", max: "0e-2147483648", inverted: true},
		// The value 1 in 65 characters: REST compares it before the core
		// sees its length, so the pair answers REST's range error.
		{min: decimal65, max: "0.5", inverted: true},
	} {
		t.Run(tc.min+"_"+tc.max, func(t *testing.T) {
			t.Parallel()
			rec, svc := serveWithin(t, boundsTrades, "priceMode=between&priceMin="+tc.min+"&priceMax="+tc.max)
			if !tc.inverted {
				if rec.Code != http.StatusOK || !boundsTrades.read(svc) {
					t.Fatalf("status = %d, want 200 and a read: %s", rec.Code, rec.Body.String())
				}
				return
			}
			problem := validationProblem(t, rec.Body.Bytes())
			if rec.Code != http.StatusUnprocessableEntity || problem.Detail != "invalid priceMode range" ||
				problem.Errors[0].Pointer != "/priceMode" {
				t.Fatalf("status = %d, problem = %+v, want the priceMode range error", rec.Code, problem)
			}
			if boundsTrades.read(svc) {
				t.Fatal("the service read the list after a rejected filter")
			}
		})
	}
}

func TestListBoundsAcceptAtBounds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		list  boundsList
		query string
		check func(t *testing.T, svc *fakeService)
	}{
		{
			name: "orders", list: boundsOrders,
			query: "baseAsset=" + mostRunes + "&amountMode=lte&amountMax=" + engineDecimalMax +
				"&priceMode=between&priceMin=-" + engineDecimalMax +
				"&priceMax=0.0000000000000000000000000001&status=filled,cancelled",
			check: func(t *testing.T, svc *fakeService) {
				filter := svc.orderFilter
				if filter.BaseAsset.Fragments[0] != mostRunes || *filter.Amount.Max != engineDecimalMax ||
					len(filter.Status) != 2 {
					t.Fatalf("order filter = %+v", filter)
				}
			},
		},
		{
			name: "trades", list: boundsTrades,
			query: "account=" + mostRunes + "&quantityMode=lte&quantityMax=" + engineDecimalMax +
				"&lockPriceMode=gte&lockPriceMin=-" + engineDecimalMax,
			check: func(t *testing.T, svc *fakeService) {
				filter := svc.tradeFilter
				if filter.Account.Fragments[0] != mostRunes || *filter.Quantity.Max != engineDecimalMax ||
					*filter.LockPrice.Min != "-"+engineDecimalMax {
					t.Fatalf("trade filter = %+v", filter)
				}
			},
		},
		{
			name: "accounts", list: boundsAccounts,
			query: "code=" + fragments16 + "&blockReason=" + mostRunes +
				"&pnlMode=lte&pnlMax=" + engineDecimalMax + "&pnlCurrency=USD",
			check: func(t *testing.T, svc *fakeService) {
				filter := svc.accountFilter
				if len(filter.Code.Fragments) != 16 || filter.BlockReason.Fragments[0] != mostRunes ||
					*filter.Pnl.Range.Max != engineDecimalMax {
					t.Fatalf("account filter = %+v", filter)
				}
			},
		},
		{
			name: "groups", list: boundsGroups,
			query: "code=" + fragments16 + "&notes=" + mostRunes + "&blockReason=" + fragments16,
			check: func(t *testing.T, svc *fakeService) {
				filter := svc.groupFilter
				if len(filter.Code.Fragments) != 16 || filter.Notes.Fragments[0] != mostRunes ||
					len(filter.BlockReason.Fragments) != 16 {
					t.Fatalf("group filter = %+v", filter)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec, svc := serveWithin(t, tc.list, tc.query)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			if !tc.list.read(svc) {
				t.Fatal("the service did not read the list")
			}
			tc.check(t, svc)
		})
	}
}
