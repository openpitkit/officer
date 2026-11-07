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

package trading

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/health"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/etoro"
)

const (
	etoroTestCOID        = "AAECAwQFBgcICQoLDA0ODw"
	etoroTestUUID        = "00010203-0405-0607-0809-0a0b0c0d0e0f"
	etoroTestCredentials = `{"apiKey":"api-\"<& +/?-marker","userKey":"user-\"<& +/?-marker"}`
	etoroTestEligibility = `{"eligibilities":[{"instrumentId":1001,"allowOpenPosition":true,"allowClosePosition":true,"allowPartialClosePosition":true,"allowedOrderQuantityType":"all","unitsQuantityType":"fractional","maxUnitsPerOrder":10,"leverageConfigs":[{"settlementType":"real","direction":"long","leverageValues":[1],"isPotential":false}]}],"notFoundInstrumentIds":[]}`
	etoroTestPosition    = `{"positionID":50,"instrumentID":1001,"isBuy":true,"settlementTypeID":1,"leverage":1,"mirrorID":0,"parentPositionID":0,"units":1,"openDateTime":"2026-10-01T10:00:00Z"}`
	etoroTestLookup      = `{"orderId":9007199254740993,"action":"open","status":{"id":3},"requestedUnits":0.6,"positionExecutions":[{"positionId":3,"openingData":{"units":0.3,"avgPrice":13.000000000000000003,"executionTime":"2026-10-02T10:00:01Z","fees":0.03,"taxes":0}},{"positionId":10,"openingData":{"units":0.2,"avgPrice":12.000000000000000002,"executionTime":"2026-10-02T10:00:00Z","fees":0.02,"taxes":0}},{"positionId":2,"openingData":{"units":0.1,"avgPrice":11.000000000000000001,"executionTime":"2026-10-02T10:00:00Z","fees":0.01,"taxes":0}}]}`
	etoroTestCloseInfo   = `{"orderID":9007199254740993,"statusID":3,"positions":[{"positionID":3,"units":0.3,"rate":13.000000000000000003,"occurred":"2026-10-02T10:00:01Z"},{"positionID":10,"units":0.2,"rate":12.000000000000000002,"occurred":"2026-10-02T10:00:00Z"},{"positionID":2,"units":0.1,"rate":11.000000000000000001,"occurred":"2026-10-02T10:00:00Z"}]}`
)

var etoroTestCloseRef = fwtrading.OrderRef{ClientOrderID: etoroTestCOID, VenueOrderID: "9007199254740993", Side: domain.OrderSideSell}

type etoroTestReply struct {
	status int
	body   string
}

type etoroTestRequest struct {
	method, path, query, requestID, body string
}

type etoroTestVenue struct {
	c           *etoroConnector
	mu          sync.Mutex
	requests    []etoroTestRequest
	diagnostics []health.Diagnostic
}

func etoroTestPath(demo bool, prefix, resource string) string {
	if demo {
		return prefix + "/demo" + resource
	}
	return prefix + resource
}

func newEtoroTestVenue(t *testing.T, demo bool, overrides map[string]etoroTestReply) *etoroTestVenue {
	t.Helper()
	v := &etoroTestVenue{}
	replies := map[string]etoroTestReply{
		etoroTestPath(demo, "/api/v2/trading/info", "/eligibility"):                           {200, etoroTestEligibility},
		etoroTestPath(demo, "/api/v1/trading/info", "/portfolio"):                             {200, `{"clientPortfolio":{"positions":[` + etoroTestPosition + `]}}`},
		etoroTestPath(demo, "/api/v3/trading/execution", "/orders"):                           {202, `{"token":"t","orderId":9007199254740993,"referenceId":"` + etoroTestUUID + `"}`},
		etoroTestPath(demo, "/api/v1/trading/execution", "/market-close-orders/positions/50"): {200, `{"orderForClose":{"orderID":9007199254740993},"token":"t"}`},
		etoroTestPath(demo, "/api/v2/trading/info", "/orders:lookup"):                         {200, etoroTestLookup},
		etoroTestPath(demo, "/api/v1/trading/info", "/aggregate-portfolio"):                   {200, `{"accountCurrency":"EUR"}`},
	}
	account := "real"
	if demo {
		account = "demo"
	}
	replies["/api/v1/trading/info/"+account+"/close-orders/9007199254740993"] = etoroTestReply{200, etoroTestCloseInfo}
	for path, reply := range overrides {
		replies[path] = reply
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error("read test request failed")
		}
		v.mu.Lock()
		v.requests = append(v.requests, etoroTestRequest{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("x-request-id"), string(body)})
		v.mu.Unlock()
		reply, exists := replies[r.URL.Path]
		if !exists {
			t.Errorf("unexpected request path: %s", r.URL.Path)
			http.Error(w, "unexpected path", 500)
			return
		}
		if reply.status == -1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error("hijack test connection failed")
				return
			}
			if err := conn.Close(); err != nil {
				t.Error(err)
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(reply.status)
		_, _ = io.WriteString(w, reply.body)
	}))
	t.Cleanup(server.Close)
	mode := domain.TradingModeLive
	if demo {
		mode = domain.TradingModePaper
	}
	connector, err := newEtoroConnector(domain.TradingConnection{Mode: mode, Credentials: etoroTestCredentials}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	v.c = connector
	v.c.reportDiag = func(d health.Diagnostic) { v.diagnostics = append(v.diagnostics, d) }
	return v
}

func etoroTestOrder() fwtrading.Order {
	return fwtrading.Order{ClientOrderID: etoroTestCOID, Symbol: "1001", Side: domain.OrderSideBuy,
		AmountKind: domain.OrderAmountKindQuantity, Quantity: "0.3", Route: "{}"}
}

func (v *etoroTestVenue) recorded() []etoroTestRequest {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]etoroTestRequest(nil), v.requests...)
}

func (v *etoroTestVenue) orderRequests() []etoroTestRequest {
	var orders []etoroTestRequest
	for _, request := range v.recorded() {
		if strings.Contains(request.path, "/trading/execution") {
			orders = append(orders, request)
		}
	}
	return orders
}

func etoroAssertJSON(t *testing.T, got, want string) {
	t.Helper()
	decode := func(raw string) any {
		var value any
		d := json.NewDecoder(strings.NewReader(raw))
		d.UseNumber()
		if err := d.Decode(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if !reflect.DeepEqual(decode(got), decode(want)) {
		t.Fatalf("request JSON = %s, want %s", got, want)
	}
}

func TestEtoroPreCheckWithoutClientOrderID(t *testing.T) {
	for _, side := range []domain.OrderSide{domain.OrderSideBuy, domain.OrderSideSell} {
		for _, coid := range []string{"", "unused-invalid-coid"} {
			t.Run(string(side)+"/"+coid, func(t *testing.T) {
				v := newEtoroTestVenue(t, true, nil)
				order := etoroTestOrder()
				order.Side, order.ClientOrderID = side, coid
				if err := v.c.PreCheck(context.Background(), order); err != nil {
					t.Fatalf("PreCheck must ignore ClientOrderID: %v", err)
				}
				requests := v.recorded()
				if len(requests) == 0 || requests[0].path != "/api/v2/trading/info/demo/eligibility" || requests[0].method != "POST" {
					t.Fatalf("PreCheck eligibility request = %+v", requests)
				}
				etoroAssertJSON(t, requests[0].body, `{"instrumentIds":[1001]}`)
				if len(v.orderRequests()) != 0 {
					t.Fatal("PreCheck reached an order endpoint")
				}
			})
		}
	}
}

func TestEtoroSendInvalidClientOrderID(t *testing.T) {
	for _, coid := range []string{"", "invalid!", base64.RawURLEncoding.EncodeToString(make([]byte, 15)), base64.RawURLEncoding.EncodeToString(make([]byte, 17))} {
		for _, side := range []domain.OrderSide{domain.OrderSideBuy, domain.OrderSideSell} {
			t.Run(string(side)+"/"+coid, func(t *testing.T) {
				v := newEtoroTestVenue(t, true, nil)
				order := etoroTestOrder()
				order.Side, order.ClientOrderID = side, coid
				if _, err := v.c.Send(context.Background(), order); !errors.Is(err, fwtrading.ErrRejected) {
					t.Fatalf("invalid ClientOrderID must be rejected: %v", err)
				}
				if len(v.recorded()) != 0 {
					t.Fatal("invalid ClientOrderID made a request")
				}
			})
		}
	}
}

func TestEtoroSellSendPreOrderFailures(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply etoroTestReply
		want  error
	}{
		{"429", etoroTestReply{429, `{}`}, fwtrading.ErrRejected},
		{"500", etoroTestReply{500, `{}`}, fwtrading.ErrRejected},
		{"transport", etoroTestReply{-1, ``}, fwtrading.ErrRejected},
		{"unreadable", etoroTestReply{200, `{`}, fwtrading.ErrRejected},
		{"shape", etoroTestReply{200, `{}`}, fwtrading.ErrRejected},
		{"no-position", etoroTestReply{200, `{"clientPortfolio":{"positions":[]}}`}, fwtrading.ErrUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newEtoroTestVenue(t, true, map[string]etoroTestReply{"/api/v1/trading/info/demo/portfolio": tc.reply})
			order := etoroTestOrder()
			order.Side = domain.OrderSideSell
			_, err := v.c.Send(context.Background(), order)
			if !errors.Is(err, tc.want) {
				t.Fatalf("pre-order failure must be definitive: %v, want %v", err, tc.want)
			}
			if tc.want == fwtrading.ErrRejected && !strings.Contains(err.Error(), "no order request was made") {
				t.Fatalf("pre-order rejection must explain that no order request was made: %v", err)
			}
			if len(v.orderRequests()) != 0 {
				t.Fatal("pre-order failure made an order request")
			}
			for _, request := range v.recorded() {
				if strings.HasSuffix(request.path, "/eligibility") {
					t.Fatal("Send repeated eligibility")
				}
			}
		})
	}
}

func TestEtoroFilledUnderExecution(t *testing.T) {
	lookup := strings.Replace(etoroTestLookup, `"requestedUnits":0.6`, `"requestedUnits":1`, 1)
	v := newEtoroTestVenue(t, true, map[string]etoroTestReply{"/api/v2/trading/info/demo/orders:lookup": {200, lookup}})
	snapshot, found, err := v.c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: etoroTestCOID, Side: domain.OrderSideBuy})
	if !found || !errors.Is(err, domain.ErrUpstream) || len(v.diagnostics) != 1 || len(snapshot.Fills) != 0 {
		t.Fatalf("filled under-execution must fail closed: found=%t err=%v diagnostics=%d fills=%d", found, err, len(v.diagnostics), len(snapshot.Fills))
	}
	if v.diagnostics[0].Code != health.CodeUnparsableData || !strings.Contains(v.diagnostics[0].Detail, "requestedUnits") {
		t.Fatalf("under-execution diagnostic = %+v", v.diagnostics)
	}
}

func TestEtoroLookupRoutesBySide(t *testing.T) {
	t.Run("buy-close-action", func(t *testing.T) {
		v := newEtoroTestVenue(t, true, map[string]etoroTestReply{
			"/api/v2/trading/info/demo/orders:lookup": {200, strings.Replace(etoroTestLookup, `"action":"open"`, `"action":"close"`, 1)},
		})
		_, found, err := v.c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: etoroTestCOID, Side: domain.OrderSideBuy})
		if !found || !errors.Is(err, domain.ErrUpstream) || len(v.diagnostics) != 1 || len(v.recorded()) != 1 {
			t.Fatalf("a buy resolved as a close must fail closed: found=%t err=%v diagnostics=%+v", found, err, v.diagnostics)
		}
	})
	t.Run("sell-without-venue-id", func(t *testing.T) {
		v := newEtoroTestVenue(t, true, nil)
		_, found, err := v.c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: etoroTestCOID, Side: domain.OrderSideSell})
		if !found || !errors.Is(err, domain.ErrUpstream) || len(v.recorded()) != 0 ||
			len(v.diagnostics) != 1 || v.diagnostics[0].Detail != etoroLookupUnresolved().Detail {
			t.Fatalf("an unacknowledged close must stay unresolved without a request: found=%t err=%v diagnostics=%+v", found, err, v.diagnostics)
		}
	})
	t.Run("no-side", func(t *testing.T) {
		v := newEtoroTestVenue(t, true, nil)
		_, found, err := v.c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: etoroTestCOID, VenueOrderID: "9007199254740993"})
		if !found || !errors.Is(err, domain.ErrInvalid) || len(v.recorded()) != 0 {
			t.Fatalf("a lookup without a side must fail without a request: found=%t err=%v", found, err)
		}
	})
}

func TestEtoroCloseNotFilled(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   fwtrading.VenueStatus
	}{
		{1, fwtrading.VenueStatusOpen}, {2, fwtrading.VenueStatusOpen}, {6, fwtrading.VenueStatusOpen},
		{11, fwtrading.VenueStatusOpen}, {4, fwtrading.VenueStatusCancelled},
		{7, fwtrading.VenueStatusCancelled}, {8, fwtrading.VenueStatusCancelled},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			info := fmt.Sprintf(`{"orderID":9007199254740993,"statusID":%d,"errorMessage":"remaining amount 0 < 1","positions":[{"positionID":50}]}`, tc.status)
			v := newEtoroTestVenue(t, true, map[string]etoroTestReply{
				"/api/v1/trading/info/demo/close-orders/9007199254740993": {200, info},
			})
			snapshot, found, err := v.c.Lookup(context.Background(), etoroTestCloseRef)
			if err != nil || !found || snapshot.Status != tc.want || snapshot.FilledQuantity != "0" || len(snapshot.Fills) != 0 {
				t.Fatalf("unfilled close lookup = %+v, found=%t err=%v", snapshot, found, err)
			}
			wantReason := ""
			if tc.status == 4 {
				wantReason = "remaining amount 0 < 1"
			}
			if snapshot.Reason != wantReason {
				t.Fatalf("close reason = %q, want %q", snapshot.Reason, wantReason)
			}
		})
	}
	for _, status := range []int{5, 9, 10} {
		t.Run(fmt.Sprintf("executions-%d", status), func(t *testing.T) {
			v := newEtoroTestVenue(t, true, map[string]etoroTestReply{
				"/api/v1/trading/info/demo/close-orders/9007199254740993": {200, strings.Replace(etoroTestCloseInfo, `"statusID":3`, fmt.Sprintf(`"statusID":%d`, status), 1)},
			})
			snapshot, found, err := v.c.Lookup(context.Background(), etoroTestCloseRef)
			if !found || !errors.Is(err, domain.ErrUpstream) || len(snapshot.Fills) != 0 || len(v.diagnostics) != 1 {
				t.Fatalf("close executions before fill must fail closed: %+v found=%t err=%v", snapshot, found, err)
			}
		})
	}
	t.Run("unknown-status", func(t *testing.T) {
		v := newEtoroTestVenue(t, true, map[string]etoroTestReply{
			"/api/v1/trading/info/demo/close-orders/9007199254740993": {200, strings.Replace(etoroTestCloseInfo, `"statusID":3`, `"statusID":99`, 1)},
		})
		if _, found, err := v.c.Lookup(context.Background(), etoroTestCloseRef); !found || !errors.Is(err, domain.ErrUpstream) || len(v.diagnostics) != 1 {
			t.Fatalf("unknown close status must fail closed: found=%t err=%v", found, err)
		}
	})
	t.Run("filled-without-executions", func(t *testing.T) {
		v := newEtoroTestVenue(t, true, map[string]etoroTestReply{
			"/api/v1/trading/info/demo/close-orders/9007199254740993": {200, `{"orderID":9007199254740993,"statusID":3,"positions":[]}`},
		})
		if _, found, err := v.c.Lookup(context.Background(), etoroTestCloseRef); !found || !errors.Is(err, domain.ErrUpstream) || len(v.diagnostics) != 1 {
			t.Fatalf("filled close without executions must fail closed: found=%t err=%v", found, err)
		}
	})
}

func TestEtoroLookupFailureDiagnostics(t *testing.T) {
	for _, site := range []string{"lookup", "close", "currency"} {
		for _, tc := range []struct {
			name  string
			reply etoroTestReply
			count int
		}{
			{"transport", etoroTestReply{-1, ``}, 0},
			{"400", etoroTestReply{400, `{}`}, 0},
			{"429", etoroTestReply{429, `{}`}, 0},
			{"500", etoroTestReply{500, `{}`}, 0},
			{"decode", etoroTestReply{200, `{`}, 1},
			{"shape", etoroTestReply{200, `{"orderId":9007199254740993}`}, 1},
			{"not-found", etoroTestReply{404, `{}`}, 1},
		} {
			t.Run(site+"/"+tc.name, func(t *testing.T) {
				path := "/api/v2/trading/info/demo/orders:lookup"
				ref := fwtrading.OrderRef{ClientOrderID: etoroTestCOID, Side: domain.OrderSideBuy}
				if site == "close" {
					path, ref = "/api/v1/trading/info/demo/close-orders/9007199254740993", etoroTestCloseRef
				}
				if site == "currency" {
					path = "/api/v1/trading/info/demo/aggregate-portfolio"
				}
				v := newEtoroTestVenue(t, true, map[string]etoroTestReply{path: tc.reply})
				_, found, err := v.c.Lookup(context.Background(), ref)
				wantCount := tc.count
				if site == "currency" && tc.name == "not-found" {
					wantCount = 0
				}
				if !found || err == nil || len(v.diagnostics) != wantCount {
					t.Fatalf("%s/%s diagnostics=%d, want %d; found=%t err=%v", site, tc.name, len(v.diagnostics), wantCount, found, err)
				}
				if tc.name == "not-found" && site != "currency" && v.diagnostics[0].Detail != etoroLookupUnresolved().Detail {
					t.Fatalf("not-found must emit only the unresolved diagnostic: %+v", v.diagnostics)
				}
				if wantCount == 1 && !errors.Is(err, domain.ErrUpstream) {
					t.Fatalf("decode/shape failure must wrap ErrUpstream: %v", err)
				}
			})
		}
	}
}

func TestEtoroLookupResponseErrors(t *testing.T) {
	const lookupPath = "/api/v2/trading/info/demo/orders:lookup"
	buy := fwtrading.OrderRef{ClientOrderID: etoroTestCOID, Side: domain.OrderSideBuy}
	for _, site := range []struct {
		name   string
		path   string
		ref    fwtrading.OrderRef
		detail string
	}{
		{"lookup", lookupPath, buy, "An eToro order lookup could not be read."},
		{"close", "/api/v1/trading/info/demo/close-orders/9007199254740993", etoroTestCloseRef,
			"An eToro close-order response could not be read."},
		{"currency", "/api/v1/trading/info/demo/aggregate-portfolio", buy,
			"An eToro aggregate portfolio could not be read."},
	} {
		for _, tc := range []struct {
			name string
			body string
		}{
			{"body-deadline", ""},
			{"syntax", `{"invalid":!}`},
			{"type", `[]`},
			{"empty", ""},
			{"truncated", `{`},
		} {
			t.Run(site.name+"/"+tc.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.URL.Path == site.path {
						w.WriteHeader(http.StatusOK)
						if tc.name == "body-deadline" {
							w.(http.Flusher).Flush()
							<-r.Context().Done()
							return
						}
						_, _ = io.WriteString(w, tc.body)
						return
					}
					if r.URL.Path != lookupPath {
						t.Errorf("unexpected request path: %s", r.URL.Path)
						http.Error(w, "unexpected path", http.StatusInternalServerError)
						return
					}
					_, _ = io.WriteString(w, etoroTestLookup)
				}))
				defer server.Close()
				c, err := newEtoroConnector(domain.TradingConnection{
					Mode: domain.TradingModePaper, Credentials: etoroTestCredentials,
				}, server.URL)
				if err != nil {
					t.Fatal(err)
				}
				var diagnostics []health.Diagnostic
				c.reportDiag = func(d health.Diagnostic) { diagnostics = append(diagnostics, d) }
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_, found, err := c.Lookup(ctx, site.ref)
				if !found || err == nil {
					t.Fatalf("response failure must leave the order pending: found=%t err=%v", found, err)
				}
				if tc.name == "body-deadline" {
					if len(diagnostics) != 0 {
						t.Errorf("body deadline emitted %d diagnostics, want 0", len(diagnostics))
					}
					if !errors.Is(err, context.DeadlineExceeded) {
						t.Errorf("body deadline cause was lost: %v", err)
					}
					if errors.Is(err, domain.ErrUpstream) || !strings.Contains(err.Error(), "decode response") {
						t.Errorf("200 body deadline must remain an undiagnosed read error: %v", err)
					}
					return
				}
				if len(diagnostics) != 1 || diagnostics[0].Code != health.CodeUnparsableData || diagnostics[0].Detail != site.detail {
					t.Errorf("decode failure diagnostics=%+v, want one unparsable diagnostic with detail %q", diagnostics, site.detail)
				}
				if !errors.Is(err, domain.ErrUpstream) {
					t.Errorf("decode failure must wrap ErrUpstream: %v", err)
				}
				var syntaxErr *json.SyntaxError
				var typeErr *json.UnmarshalTypeError
				switch tc.name {
				case "syntax":
					if !errors.As(err, &syntaxErr) {
						t.Errorf("JSON syntax cause was lost: %v", err)
					}
				case "type":
					if !errors.As(err, &typeErr) {
						t.Errorf("JSON type cause was lost: %v", err)
					}
				case "empty":
					if !errors.Is(err, io.EOF) {
						t.Errorf("empty JSON cause was lost: %v", err)
					}
				case "truncated":
					if !errors.Is(err, io.ErrUnexpectedEOF) {
						t.Errorf("truncated JSON cause was lost: %v", err)
					}
				}
			})
		}
	}
}

func TestVenueLookupDiagnosticContract(t *testing.T) {
	want := health.Diagnostic{
		Level: health.DiagError, Code: health.CodeUnparsableData,
		Kind: health.DiagKindProvider, Title: "Unknown order data from the venue",
		Detail: "An eToro order lookup returned an unknown status.",
		Remediation: "Officer keeps the order pending and retries the " +
			"lookup; the order is not reconciled until the venue reports " +
			"data Officer knows. This is a venue or Officer issue, not your " +
			"configuration - please report it.",
	}
	if got := venueLookupUnparsable(want.Detail); !reflect.DeepEqual(got, want) {
		t.Fatalf("shared lookup diagnostic = %+v, want %+v", got, want)
	}
	v := newEtoroTestVenue(t, true, map[string]etoroTestReply{
		"/api/v2/trading/info/demo/orders:lookup": {200, strings.Replace(etoroTestLookup, `"id":3`, `"id":99`, 1)},
	})
	if _, _, err := v.c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: etoroTestCOID, Side: domain.OrderSideBuy}); !errors.Is(err, domain.ErrUpstream) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(v.diagnostics, []health.Diagnostic{want}) {
		t.Fatalf("eToro must use the shared lookup diagnostic: %+v", v.diagnostics)
	}
}

func TestEtoroInteger(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
		positive  bool
	}{
		{"9007199254740993", "9007199254740993", true},
		{"1.0", "1", true}, {"1e3", "1000", true}, {"0", "0", false},
		{"1.5", "", true}, {"0", "", true}, {"-1", "", false}, {"", "", true},
	} {
		t.Run(fmt.Sprintf("%s/positive=%t", tc.raw, tc.positive), func(t *testing.T) {
			value, err := etoroInteger(json.Number(tc.raw), "test integer", tc.positive)
			if tc.want == "" {
				if !errors.Is(err, domain.ErrUpstream) {
					t.Fatalf("invalid integer %q must return ErrUpstream: value=%s err=%v", tc.raw, value, err)
				}
			} else if err != nil || value.String() != tc.want || !value.IsInteger() || value.LessThan(decimal.Zero) {
				t.Fatalf("integer %q = %s, %v; want exact decimal %s", tc.raw, value, err, tc.want)
			}
		})
	}
}

func TestEtoroConstructor(t *testing.T) {
	for _, tc := range []struct {
		mode        domain.TradingMode
		credentials string
		wantErr     error
	}{
		{domain.TradingMode("unknown"), etoroTestCredentials, nil},
		{domain.TradingModePaper, `{}`, domain.ErrInvalid},
		{domain.TradingModeLive, `null`, domain.ErrInvalid},
	} {
		_, err := newEtoroConnector(domain.TradingConnection{Mode: tc.mode, Credentials: tc.credentials}, "http://127.0.0.1:1")
		if err == nil {
			t.Fatal("invalid constructor input accepted")
		}
		if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
			t.Fatalf("newEtoroConnector: got %v, want %v", err, tc.wantErr)
		}
	}
}

func TestEtoroSendBodiesAndPaths(t *testing.T) {
	for _, demo := range []bool{true, false} {
		for _, kind := range []string{"market-buy", "limit-buy", "limit-normalized", "partial-sell", "whole-sell"} {
			t.Run(fmt.Sprintf("demo=%t/%s", demo, kind), func(t *testing.T) {
				order := etoroTestOrder()
				var overrides map[string]etoroTestReply
				wantPath := etoroTestPath(demo, "/api/v3/trading/execution", "/orders")
				wantBody := `{"action":"open","transaction":"buy","instrumentId":1001,"settlementType":"real","leverage":1,"orderType":"mkt","units":0.3}`
				if strings.HasPrefix(kind, "limit-") {
					order.Route, order.LimitPrice = `{"timeInForce":"ioc"}`, "12.000000000000000001"
					wantBody = `{"action":"open","transaction":"buy","instrumentId":1001,"settlementType":"real","leverage":1,"orderType":"limitIOC","units":0.3,"limitRate":12.000000000000000001}`
					if kind == "limit-normalized" {
						order.Quantity, order.LimitPrice = "+00.3", "+0012.000000000000000001"
					}
				}
				if strings.HasSuffix(kind, "sell") {
					order.Side = domain.OrderSideSell
					wantPath = etoroTestPath(demo, "/api/v1/trading/execution", "/market-close-orders/positions/50")
					wantBody = `{"InstrumentID":1001,"UnitsToDeduct":0.3}`
					if !demo {
						wantBody = `{"InstrumentId":1001,"UnitsToDeduct":0.3}`
					}
					if kind == "whole-sell" {
						wantBody = `{"InstrumentID":1001}`
						if !demo {
							wantBody = `{"InstrumentId":1001}`
						}
						overrides = map[string]etoroTestReply{
							etoroTestPath(demo, "/api/v1/trading/info", "/portfolio"): {200,
								`{"clientPortfolio":{"positions":[` + strings.Replace(etoroTestPosition, `"units":1`, `"units":0.3`, 1) + `]}}`},
							etoroTestPath(demo, "/api/v2/trading/info", "/eligibility"): {200,
								strings.Replace(etoroTestEligibility, `"allowPartialClosePosition":true`, `"allowPartialClosePosition":false`, 1)},
						}
					}
				}
				v := newEtoroTestVenue(t, demo, overrides)
				ack, err := v.c.Send(context.Background(), order)
				if err != nil || ack.VenueOrderID != "9007199254740993" {
					t.Fatalf("send acknowledgement = %+v, %v", ack, err)
				}
				orders := v.orderRequests()
				if len(orders) != 1 || orders[0].path != wantPath || orders[0].method != "POST" {
					t.Fatalf("order endpoint requests = %+v", orders)
				}
				if orders[0].requestID != etoroTestUUID {
					t.Fatalf("x-request-id = %s, want exact UUID(ClientOrderID) %s", orders[0].requestID, etoroTestUUID)
				}
				etoroAssertJSON(t, orders[0].body, wantBody)
				requests := v.recorded()
				for _, request := range requests {
					if strings.HasSuffix(request.path, "/eligibility") {
						t.Fatalf("Send repeated eligibility request: %+v", request)
					}
				}
			})
		}
	}
}

func TestEtoroLocalRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*fwtrading.Order)
		want   error
	}{
		{"invalid-route", func(o *fwtrading.Order) { o.Route = "{" }, fwtrading.ErrRejected},
		{"null-route", func(o *fwtrading.Order) { o.Route = "null" }, fwtrading.ErrRejected},
		{"array-route", func(o *fwtrading.Order) { o.Route = "[]" }, fwtrading.ErrRejected},
		{"trailing-route", func(o *fwtrading.Order) { o.Route = "{} {}" }, fwtrading.ErrRejected},
		{"unknown-field", func(o *fwtrading.Order) { o.Route = `{"secret":"x"}` }, fwtrading.ErrRejected},
		{"unknown-tif", func(o *fwtrading.Order) { o.Route = `{"timeInForce":"day"}` }, fwtrading.ErrRejected},
		{"null-tif", func(o *fwtrading.Order) { o.Route = `{"timeInForce":null}` }, fwtrading.ErrRejected},
		{"numeric-tif", func(o *fwtrading.Order) { o.Route = `{"timeInForce":1}` }, fwtrading.ErrRejected},
		{"volume", func(o *fwtrading.Order) { o.AmountKind = domain.OrderAmountKindVolume }, fwtrading.ErrUnsupported},
		{"symbol", func(o *fwtrading.Order) { o.Symbol = "AAPL" }, fwtrading.ErrRejected},
		{"zero-symbol", func(o *fwtrading.Order) { o.Symbol = "0" }, fwtrading.ErrRejected},
		{"fractional-symbol", func(o *fwtrading.Order) { o.Symbol = "1001.1" }, fwtrading.ErrRejected},
		{"signed-symbol", func(o *fwtrading.Order) { o.Symbol = "+1001" }, fwtrading.ErrRejected},
		{"blank-symbol", func(o *fwtrading.Order) { o.Symbol = "" }, fwtrading.ErrRejected},
		{"zero-quantity", func(o *fwtrading.Order) { o.Quantity = "0" }, fwtrading.ErrRejected},
		{"negative-quantity", func(o *fwtrading.Order) { o.Quantity = "-1" }, fwtrading.ErrRejected},
		{"exponent-quantity", func(o *fwtrading.Order) { o.Quantity = "1e-1" }, fwtrading.ErrRejected},
		{"invalid-quantity", func(o *fwtrading.Order) { o.Quantity = "NaN" }, fwtrading.ErrRejected},
		{"zero-price", func(o *fwtrading.Order) { o.LimitPrice = "0"; o.Route = `{"timeInForce":"ioc"}` }, fwtrading.ErrRejected},
		{"negative-price", func(o *fwtrading.Order) { o.LimitPrice = "-1"; o.Route = `{"timeInForce":"ioc"}` }, fwtrading.ErrRejected},
		{"limit-without-ioc", func(o *fwtrading.Order) { o.LimitPrice = "10" }, fwtrading.ErrUnsupported},
		{"ioc-without-limit", func(o *fwtrading.Order) { o.Route = `{"timeInForce":"ioc"}` }, fwtrading.ErrUnsupported},
		{"limit-sell", func(o *fwtrading.Order) {
			o.Side = domain.OrderSideSell
			o.Route = `{"timeInForce":"ioc"}`
			o.LimitPrice = "10"
		}, fwtrading.ErrUnsupported},
		{"side", func(o *fwtrading.Order) { o.Side = domain.OrderSide("sellShort") }, fwtrading.ErrUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newEtoroTestVenue(t, true, nil)
			order := etoroTestOrder()
			tc.change(&order)
			if err := v.c.PreCheck(context.Background(), order); !errors.Is(err, tc.want) {
				t.Fatalf("PreCheck refusal = %v, want %v", err, tc.want)
			}
			if _, err := v.c.Send(context.Background(), order); !errors.Is(err, tc.want) {
				t.Fatalf("Send refusal = %v, want %v", err, tc.want)
			}
			if requests := v.recorded(); len(requests) != 0 {
				t.Fatalf("local refusal made %d requests, want zero", len(requests))
			}
		})
	}
}

func TestEtoroEligibilityRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, old, replacement string
		want                   error
	}{
		{"not-found", `"notFoundInstrumentIds":[]`, `"notFoundInstrumentIds":[1001]`, fwtrading.ErrRejected},
		{"absent", `"instrumentId":1001`, `"instrumentId":1002`, fwtrading.ErrRejected},
		{"open-disabled", `"allowOpenPosition":true`, `"allowOpenPosition":false`, fwtrading.ErrRejected},
		{"cfd", `"settlementType":"real"`, `"settlementType":"cfd"`, fwtrading.ErrUnsupported},
		{"demo-futures", `"settlementType":"real"`, `"settlementType":"reaL_FUTURES"`, fwtrading.ErrUnsupported},
		{"leveraged", `"leverageValues":[1]`, `"leverageValues":[2]`, fwtrading.ErrUnsupported},
		{"short", `"direction":"long"`, `"direction":"short"`, fwtrading.ErrUnsupported},
		{"potential", `"isPotential":false`, `"isPotential":true`, fwtrading.ErrUnsupported},
		{"amount-only", `"allowedOrderQuantityType":"all"`, `"allowedOrderQuantityType":"amountOnly"`, fwtrading.ErrUnsupported},
		{"whole", `"unitsQuantityType":"fractional"`, `"unitsQuantityType":"whole"`, fwtrading.ErrRejected},
		{"max", `"maxUnitsPerOrder":10`, `"maxUnitsPerOrder":0.29999999999999999999`, fwtrading.ErrRejected},
		{"missing-max", `"maxUnitsPerOrder":10,`, ``, domain.ErrUpstream},
		{"null-max", `"maxUnitsPerOrder":10`, `"maxUnitsPerOrder":null`, domain.ErrUpstream},
		{"unknown-settlement", `"settlementType":"real"`, `"settlementType":"future"`, domain.ErrUpstream},
		{"unknown-direction", `"direction":"long"`, `"direction":"future"`, domain.ErrUpstream},
		{"unknown-amount", `"allowedOrderQuantityType":"all"`, `"allowedOrderQuantityType":"future"`, domain.ErrUpstream},
		{"unknown-units", `"unitsQuantityType":"fractional"`, `"unitsQuantityType":"future"`, domain.ErrUpstream},
		{"missing-open", `"allowOpenPosition":true,`, ``, domain.ErrUpstream},
		{"missing-potential", `,"isPotential":false`, ``, domain.ErrUpstream},
		{"missing-leverages", `"leverageValues":[1],`, ``, domain.ErrUpstream},
		{"missing-configs", `"leverageConfigs":[{"settlementType":"real","direction":"long","leverageValues":[1],"isPotential":false}]`, `"leverageConfigs":null`, domain.ErrUpstream},
		{"missing-units-type", `"unitsQuantityType":"fractional",`, ``, domain.ErrUpstream},
		{"missing-amount-type", `"allowedOrderQuantityType":"all",`, ``, domain.ErrUpstream},
		{"invalid-max", `"maxUnitsPerOrder":10`, `"maxUnitsPerOrder":-1`, domain.ErrUpstream},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := strings.Replace(etoroTestEligibility, tc.old, tc.replacement, 1)
			if response == etoroTestEligibility {
				t.Fatal("eligibility fixture replacement did not apply")
			}
			v := newEtoroTestVenue(t, true, map[string]etoroTestReply{
				"/api/v2/trading/info/demo/eligibility": {200, response},
			})
			if err := v.c.PreCheck(context.Background(), etoroTestOrder()); !errors.Is(err, tc.want) {
				t.Fatalf("PreCheck venue refusal = %v, want %v", err, tc.want)
			}
			if len(v.orderRequests()) != 0 {
				t.Fatal("venue refusal reached an order endpoint")
			}
		})
	}
	for _, status := range []int{401, 500, -1} {
		t.Run(fmt.Sprintf("eligibility-error-%d", status), func(t *testing.T) {
			v := newEtoroTestVenue(t, true, map[string]etoroTestReply{
				"/api/v2/trading/info/demo/eligibility": {status, `{}`},
			})
			err := v.c.PreCheck(context.Background(), etoroTestOrder())
			if err == nil || errors.Is(err, fwtrading.ErrRejected) || errors.Is(err, fwtrading.ErrUnsupported) {
				t.Fatalf("eligibility request error must stay plain: %v", err)
			}
			if len(v.orderRequests()) != 0 {
				t.Fatal("failed eligibility request reached an order endpoint")
			}
		})
	}
}

func TestEtoroSellPositionChoice(t *testing.T) {
	positions := []string{
		strings.Replace(etoroTestPosition, `"instrumentID":1001`, `"instrumentID":1002`, 1),
		strings.Replace(etoroTestPosition, `"mirrorID":0`, `"mirrorID":9`, 1),
		strings.Replace(etoroTestPosition, `"parentPositionID":0`, `"parentPositionID":8`, 1),
		strings.Replace(etoroTestPosition, `"leverage":1`, `"leverage":2`, 1),
		strings.Replace(etoroTestPosition, `"settlementTypeID":1`, `"settlementTypeID":0`, 1),
		strings.Replace(etoroTestPosition, `"isBuy":true`, `"isBuy":false`, 1),
		strings.Replace(etoroTestPosition, `"units":1`, `"units":0.2`, 1),
		strings.Replace(strings.Replace(etoroTestPosition, `"positionID":50`, `"positionID":1`, 1), "2026-10-01", "2026-10-03", 1),
		strings.Replace(strings.Replace(etoroTestPosition, `"positionID":50`, `"positionID":10`, 1), "2026-10-01", "2026-10-02", 1),
		strings.Replace(strings.Replace(etoroTestPosition, `"positionID":50`, `"positionID":2`, 1), "2026-10-01", "2026-10-02", 1),
	}
	v := newEtoroTestVenue(t, true, map[string]etoroTestReply{
		"/api/v1/trading/info/demo/portfolio":                            {200, `{"clientPortfolio":{"positions":[` + strings.Join(positions, ",") + `]}}`},
		"/api/v1/trading/execution/demo/market-close-orders/positions/2": {200, `{"orderForClose":{"orderID":27}}`},
	})
	order := etoroTestOrder()
	order.Side = domain.OrderSideSell
	if err := v.c.PreCheck(context.Background(), order); err != nil {
		t.Fatal(err)
	}
	if _, err := v.c.Send(context.Background(), order); err != nil {
		t.Fatal(err)
	}
	orders := v.orderRequests()
	if len(orders) != 1 || !strings.HasSuffix(orders[0].path, "/positions/2") {
		t.Fatalf("sell must choose oldest qualifying position, numeric id breaks ties: %+v", orders)
	}
	etoroAssertJSON(t, orders[0].body, `{"InstrumentID":1001,"UnitsToDeduct":0.3}`)
	if len(v.recorded()) != 4 {
		t.Fatalf("Send must re-read positions after PreCheck without repeating eligibility, requests=%d", len(v.recorded()))
	}
}

func TestEtoroSellReResolvesPosition(t *testing.T) {
	var mu sync.Mutex
	portfolioCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/trading/info/demo/eligibility":
			_, _ = io.WriteString(w, etoroTestEligibility)
		case "/api/v1/trading/info/demo/portfolio":
			mu.Lock()
			portfolioCalls++
			call := portfolioCalls
			mu.Unlock()
			position := etoroTestPosition
			if call == 2 {
				position = strings.Replace(position, `"positionID":50`, `"positionID":51`, 1)
			}
			_, _ = io.WriteString(w, `{"clientPortfolio":{"positions":[`+position+`]}}`)
		case "/api/v1/trading/execution/demo/market-close-orders/positions/51":
			_, _ = io.WriteString(w, `{"orderForClose":{"orderID":27}}`)
		default:
			t.Errorf("Send used a stale position or unexpected path: %s", r.URL.Path)
			http.Error(w, "stale position", 500)
		}
	}))
	defer server.Close()
	c, err := newEtoroConnector(domain.TradingConnection{Mode: domain.TradingModePaper, Credentials: etoroTestCredentials}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	order := etoroTestOrder()
	order.Side = domain.OrderSideSell
	if err := c.PreCheck(context.Background(), order); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Send(context.Background(), order); err != nil {
		t.Fatalf("Send must use the newly resolved position: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if portfolioCalls != 2 {
		t.Fatalf("portfolio resolved %d times, want once in each PreCheck and Send", portfolioCalls)
	}
}

func TestEtoroSellRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, eligibility, portfolio string
		want                         error
	}{
		{"no-single-position", etoroTestEligibility, `{"clientPortfolio":{"positions":[` + strings.Replace(etoroTestPosition, `"units":1`, `"units":0.2`, 1) + `,` + strings.Replace(etoroTestPosition, `"units":1`, `"units":0.1`, 1) + `]}}`, fwtrading.ErrUnsupported},
		{"empty", etoroTestEligibility, `{"clientPortfolio":{"positions":[]}}`, fwtrading.ErrUnsupported},
		{"close-disabled", strings.Replace(etoroTestEligibility, `"allowClosePosition":true`, `"allowClosePosition":false`, 1), `{"clientPortfolio":{"positions":[` + etoroTestPosition + `]}}`, fwtrading.ErrRejected},
		{"partial-disabled", strings.Replace(etoroTestEligibility, `"allowPartialClosePosition":true`, `"allowPartialClosePosition":false`, 1), `{"clientPortfolio":{"positions":[` + etoroTestPosition + `]}}`, fwtrading.ErrUnsupported},
		{"missing-close", strings.Replace(etoroTestEligibility, `"allowClosePosition":true,`, ``, 1), `{"clientPortfolio":{"positions":[` + etoroTestPosition + `]}}`, domain.ErrUpstream},
		{"missing-partial", strings.Replace(etoroTestEligibility, `"allowPartialClosePosition":true,`, ``, 1), `{"clientPortfolio":{"positions":[` + etoroTestPosition + `]}}`, domain.ErrUpstream},
		{"missing-positions", etoroTestEligibility, `{"clientPortfolio":{}}`, domain.ErrUpstream},
		{"unknown-settlement", etoroTestEligibility, `{"clientPortfolio":{"positions":[` + strings.Replace(etoroTestPosition, `"settlementTypeID":1`, `"settlementTypeID":99`, 1) + `]}}`, domain.ErrUpstream},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newEtoroTestVenue(t, true, map[string]etoroTestReply{
				"/api/v2/trading/info/demo/eligibility": {200, tc.eligibility},
				"/api/v1/trading/info/demo/portfolio":   {200, tc.portfolio},
			})
			order := etoroTestOrder()
			order.Side = domain.OrderSideSell
			err := v.c.PreCheck(context.Background(), order)
			if !errors.Is(err, tc.want) {
				t.Fatalf("sell PreCheck refusal = %v, want %v", err, tc.want)
			}
			if (tc.name == "no-single-position" || tc.name == "empty") && !strings.Contains(err.Error(), "no single eToro position covers the sell") {
				t.Fatalf("no-position refusal must use fixed detail: %v", err)
			}
			if len(v.orderRequests()) != 0 {
				t.Fatal("sell refusal reached an order endpoint")
			}
		})
	}
}

func TestEtoroSendClassification(t *testing.T) {
	for _, side := range []domain.OrderSide{domain.OrderSideBuy, domain.OrderSideSell} {
		for _, status := range []int{400, 401, 403, 404, 408, 422, 429, 500, 200, 202, -1} {
			t.Run(fmt.Sprintf("%s/%d", side, status), func(t *testing.T) {
				path := "/api/v3/trading/execution/demo/orders"
				if side == domain.OrderSideSell {
					path = "/api/v1/trading/execution/demo/market-close-orders/positions/50"
				}
				v := newEtoroTestVenue(t, true, map[string]etoroTestReply{path: {status, `{}`}})
				order := etoroTestOrder()
				order.Side = side
				_, err := v.c.Send(context.Background(), order)
				rejected := status >= 400 && status < 500 && status != 408
				if err == nil || errors.Is(err, fwtrading.ErrRejected) != rejected {
					t.Fatalf("Send status %d: rejected=%t, error=%v", status, rejected, err)
				}
				if !rejected && !strings.Contains(err.Error(), "etoro: send outcome unknown:") {
					t.Fatalf("ambiguous Send lost outcome-unknown marker: %v", err)
				}
				if len(v.orderRequests()) != 1 {
					t.Fatalf("Send must make exactly one order request, got %d", len(v.orderRequests()))
				}
			})
		}
	}
}

func TestEtoroLookupQueriesAndStatuses(t *testing.T) {
	for _, demo := range []bool{true, false} {
		for _, byOrderID := range []bool{true, false} {
			for id := 1; id <= 12; id++ {
				t.Run(fmt.Sprintf("demo=%t/order-id=%t/status=%d", demo, byOrderID, id), func(t *testing.T) {
					lookup := strings.Replace(etoroTestLookup, `"id":3`, fmt.Sprintf(`"id":%d`, id), 1)
					v := newEtoroTestVenue(t, demo, map[string]etoroTestReply{
						etoroTestPath(demo, "/api/v2/trading/info", "/orders:lookup"): {200, lookup},
					})
					ref := fwtrading.OrderRef{ClientOrderID: etoroTestCOID, Side: domain.OrderSideBuy}
					wantQuery := url.Values{"referenceId": {etoroTestUUID}}
					if byOrderID {
						ref.VenueOrderID = "9007199254740993"
						ref.ClientOrderID = "unused-invalid-coid"
						wantQuery = url.Values{"orderId": {"9007199254740993"}}
					}
					snapshot, found, err := v.c.Lookup(context.Background(), ref)
					if err != nil || !found || snapshot.VenueOrderID != "9007199254740993" {
						t.Fatalf("lookup = %+v, found=%t, err=%v", snapshot, found, err)
					}
					wantStatus := fwtrading.VenueStatusOpen
					switch id {
					case 3:
						wantStatus = fwtrading.VenueStatusFilled
					case 4, 7, 8, 9, 10:
						wantStatus = fwtrading.VenueStatusCancelled
					}
					if snapshot.Status != wantStatus {
						t.Fatalf("status %d = %s, want %s", id, snapshot.Status, wantStatus)
					}
					for i, fill := range snapshot.Fills {
						wantFinal := i == 2 && wantStatus == fwtrading.VenueStatusFilled
						if fill.Final != wantFinal {
							t.Fatalf("status %d fill %d Final=%t, want %t", id, i, fill.Final, wantFinal)
						}
					}
					requests := v.recorded()
					if len(requests) != 2 || requests[0].query != wantQuery.Encode() || requests[0].method != "GET" ||
						requests[0].path != etoroTestPath(demo, "/api/v2/trading/info", "/orders:lookup") ||
						requests[1].path != etoroTestPath(demo, "/api/v1/trading/info", "/aggregate-portfolio") || requests[1].method != "GET" {
						t.Fatalf("lookup must use one query parameter and one aggregate read: %+v", requests)
					}
				})
			}
		}
	}
}

func etoroAssertFills(t *testing.T, snapshot fwtrading.Snapshot, commission bool, lastFinal bool) {
	t.Helper()
	if snapshot.FilledQuantity != "0.6" || len(snapshot.Fills) != 3 {
		t.Fatalf("execution prefix = %s, %d fills; want exact 0.6, 3 fills", snapshot.FilledQuantity, len(snapshot.Fills))
	}
	for i, want := range []struct{ quantity, price, cum, leaves, fee, at string }{
		{"0.1", "11.000000000000000001", "0.1", "0.5", "0.01", "2026-10-02T10:00:00Z"},
		{"0.2", "12.000000000000000002", "0.3", "0.3", "0.02", "2026-10-02T10:00:00Z"},
		{"0.3", "13.000000000000000003", "0.6", "0", "0.03", "2026-10-02T10:00:01Z"},
	} {
		fill := snapshot.Fills[i]
		if fill.Quantity != want.quantity || fill.Price != want.price || fill.CumQuantity != want.cum || fill.LeavesQuantity != want.leaves ||
			fill.At.Format(time.RFC3339Nano) != want.at || fill.Final != (i == 2 && lastFinal) {
			t.Fatalf("fill %d ordering/decimals/cumulative/Final = %+v, want %+v", i, fill, want)
		}
		if commission {
			if fill.Commission == nil || fill.Commission.Currency != "EUR" || fill.Commission.Amount != want.fee {
				t.Fatalf("fill %d commission = %+v, want %s EUR from account", i, fill.Commission, want.fee)
			}
		} else if fill.Commission != nil {
			t.Fatalf("close fill %d inferred a fee: %+v", i, fill.Commission)
		}
	}
}

func TestEtoroOpenFills(t *testing.T) {
	for _, status := range []int{3, 5, 9, 10} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			lookup := strings.Replace(etoroTestLookup, `"id":3`, fmt.Sprintf(`"id":%d`, status), 1)
			if status == 9 || status == 10 {
				lookup = strings.Replace(lookup, `"requestedUnits":0.6`, `"requestedUnits":1`, 1)
			}
			v := newEtoroTestVenue(t, true, map[string]etoroTestReply{"/api/v2/trading/info/demo/orders:lookup": {200, lookup}})
			snapshot, found, err := v.c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: etoroTestCOID, Side: domain.OrderSideBuy})
			if err != nil || !found {
				t.Fatalf("open fills lookup: found=%t, err=%v", found, err)
			}
			if status == 9 || status == 10 {
				if snapshot.Status != fwtrading.VenueStatusCancelled || snapshot.FilledQuantity != "0.6" || len(snapshot.Fills) != 3 ||
					snapshot.Fills[2].LeavesQuantity != "0.4" || snapshot.Fills[2].Final {
					t.Fatalf("IOC cancelled partial lost filled prefix or exact leaves: %+v", snapshot)
				}
			} else {
				etoroAssertFills(t, snapshot, true, status == 3)
			}
		})
	}
}

func TestEtoroLookupUnresolved(t *testing.T) {
	for _, byOrderID := range []bool{true, false} {
		for _, tc := range []etoroTestReply{
			{404, `{}`}, {200, `{}`}, {200, `null`}, {200, `{"found":false}`}, {200, `{"orderId":null}`}, {200, `{"orderId":0}`},
		} {
			t.Run(fmt.Sprintf("order-id=%t/%d/%s", byOrderID, tc.status, tc.body), func(t *testing.T) {
				v := newEtoroTestVenue(t, true, map[string]etoroTestReply{"/api/v2/trading/info/demo/orders:lookup": tc})
				ref := fwtrading.OrderRef{ClientOrderID: etoroTestCOID, Side: domain.OrderSideBuy}
				if byOrderID {
					ref.VenueOrderID = "27"
				}
				_, found, err := v.c.Lookup(context.Background(), ref)
				if !found || !errors.Is(err, domain.ErrUpstream) || !strings.Contains(err.Error(), "etoro: order not resolved") || !strings.Contains(err.Error(), "probe P2/P6") {
					t.Fatalf("unresolved order must stay pending: found=%t, err=%v", found, err)
				}
				if len(v.diagnostics) != 1 || v.diagnostics[0].Detail != etoroLookupUnresolved().Detail {
					t.Fatalf("unresolved order diagnostic = %+v", v.diagnostics)
				}
			})
		}
	}
	v := newEtoroTestVenue(t, true, nil)
	v.c.reportDiag = nil
	if _, found, err := v.c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: etoroTestCOID, Side: domain.OrderSideBuy}); err == nil || !found {
		t.Fatalf("lookup without reporter: found=%t err=%v", found, err)
	}
	if len(v.recorded()) != 0 {
		t.Fatal("lookup without reporter made a request")
	}
	v.c.reportDiag = func(d health.Diagnostic) { v.diagnostics = append(v.diagnostics, d) }
	if _, found, err := v.c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: "bad!", Side: domain.OrderSideBuy}); err == nil || !found {
		t.Fatalf("invalid lookup coid: found=%t err=%v", found, err)
	}
	if len(v.recorded()) != 0 {
		t.Fatal("invalid lookup coid made a request")
	}
}

func TestEtoroMissingExecutionRecords(t *testing.T) {
	for _, records := range []string{``, `,"positionExecutions":null`, `,"positionExecutions":[]`} {
		t.Run(records, func(t *testing.T) {
			lookup := `{"orderId":9007199254740993,"action":"open","status":{"id":3},"requestedUnits":1` + records + `}`
			v := newEtoroTestVenue(t, true, map[string]etoroTestReply{"/api/v2/trading/info/demo/orders:lookup": {200, lookup}})
			_, found, err := v.c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: etoroTestCOID, Side: domain.OrderSideBuy})
			if !found || !errors.Is(err, domain.ErrUpstream) || len(v.diagnostics) == 0 {
				t.Fatalf("filled status without executions must fail loudly: found=%t err=%v", found, err)
			}
		})
	}
}

func TestEtoroLookupInvalidData(t *testing.T) {
	for _, tc := range []struct{ name, old, replacement string }{
		{"unknown-status", `"id":3`, `"id":987654321`},
		{"fractional-order-id", `"orderId":9007199254740993`, `"orderId":1.5`},
		{"negative-order-id", `"orderId":9007199254740993`, `"orderId":-1`},
		{"missing-status", `"status":{"id":3}`, `"status":null`},
		{"missing-status-id", `"id":3`, `"other":3`},
		{"unknown-action", `"action":"open"`, `"action":"future"`},
		{"missing-requested", `"requestedUnits":0.6,`, ``},
		{"negative-requested", `"requestedUnits":0.6`, `"requestedUnits":-1`},
		{"overfilled", `"requestedUnits":0.6`, `"requestedUnits":0.5`},
		{"missing-units", `"units":0.3,`, ``},
		{"missing-price", `"avgPrice":13.000000000000000003,`, ``},
		{"missing-time", `"executionTime":"2026-10-02T10:00:01Z",`, ``},
		{"missing-fees", `"fees":0.03,`, ``},
		{"missing-taxes", `,"taxes":0`, ``},
		{"taxes", `"taxes":0`, `"taxes":0.000000000000000001`},
		{"negative-units", `"units":0.3`, `"units":-0.3`},
		{"zero-price", `"avgPrice":13.000000000000000003`, `"avgPrice":0`},
		{"missing-position-id", `"positionId":3,`, ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := strings.Replace(etoroTestLookup, tc.old, tc.replacement, 1)
			if lookup == etoroTestLookup {
				t.Fatal("lookup fixture replacement did not apply")
			}
			v := newEtoroTestVenue(t, true, map[string]etoroTestReply{"/api/v2/trading/info/demo/orders:lookup": {200, lookup}})
			_, found, err := v.c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: etoroTestCOID, Side: domain.OrderSideBuy})
			if !found || !errors.Is(err, domain.ErrUpstream) || len(v.diagnostics) != 1 {
				t.Fatalf("invalid lookup must error and diagnose: found=%t err=%v diagnostics=%+v", found, err, v.diagnostics)
			}
			if v.diagnostics[0].Detail == etoroLookupUnresolved().Detail {
				t.Fatal("invalid lookup data must not be diagnosed as not-found")
			}
			if tc.name == "unknown-status" && strings.Contains(v.diagnostics[0].Detail, "987654321") {
				t.Fatal("unknown status id was embedded in diagnostic detail")
			}
			if tc.name == "taxes" && !strings.Contains(err.Error(), "probe P5") {
				t.Fatalf("non-zero taxes error must name probe P5: %v", err)
			}
		})
	}
	for _, raw := range []string{`{}`, `{"accountCurrency":null}`, `{"accountCurrency":""}`, `{"accountCurrency":"  "}`, `{"accountCurrency":"eur"}`} {
		t.Run("currency/"+raw, func(t *testing.T) {
			v := newEtoroTestVenue(t, true, map[string]etoroTestReply{"/api/v1/trading/info/demo/aggregate-portfolio": {200, raw}})
			_, found, err := v.c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: etoroTestCOID, Side: domain.OrderSideBuy})
			if !found || !errors.Is(err, domain.ErrUpstream) || !strings.Contains(err.Error(), "accountCurrency") || len(v.diagnostics) == 0 {
				t.Fatalf("missing/blank currency must error and diagnose: found=%t err=%v", found, err)
			}
		})
	}
}

func TestEtoroCloseFills(t *testing.T) {
	for _, demo := range []bool{true, false} {
		t.Run(fmt.Sprintf("demo=%t", demo), func(t *testing.T) {
			v := newEtoroTestVenue(t, demo, nil)
			snapshot, found, err := v.c.Lookup(context.Background(), etoroTestCloseRef)
			if err != nil || !found {
				t.Fatalf("close lookup: found=%t err=%v", found, err)
			}
			etoroAssertFills(t, snapshot, false, true)
			account := "real"
			if demo {
				account = "demo"
			}
			requests := v.recorded()
			if len(requests) != 1 || requests[0].method != "GET" || requests[0].path != "/api/v1/trading/info/"+account+"/close-orders/9007199254740993" {
				t.Fatalf("a close must be one close-orders read on the explicit demo/real path: %+v", requests)
			}
		})
	}
	for _, tc := range []struct{ name, old, replacement string }{
		{"missing-units", `"units":0.3,`, ``},
		{"missing-rate", `"rate":13.000000000000000003,`, ``},
		{"missing-occurred", `,"occurred":"2026-10-02T10:00:01Z"`, ``},
		{"missing-id", `"positionID":3,`, ``},
		{"negative-units", `"units":0.3`, `"units":-1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newEtoroTestVenue(t, true, map[string]etoroTestReply{
				"/api/v1/trading/info/demo/close-orders/9007199254740993": {200, strings.Replace(etoroTestCloseInfo, tc.old, tc.replacement, 1)},
			})
			_, found, err := v.c.Lookup(context.Background(), etoroTestCloseRef)
			if !found || !errors.Is(err, domain.ErrUpstream) || len(v.diagnostics) == 0 {
				t.Fatalf("missing close execution must not be skipped: found=%t err=%v", found, err)
			}
		})
	}
}

func TestEtoroNoFillsFromTotals(t *testing.T) {
	v := newEtoroTestVenue(t, true, map[string]etoroTestReply{
		"/api/v2/trading/info/demo/orders:lookup": {200, `{"orderId":27,"action":"open","status":{"id":1},"requestedUnits":1,"requestedAmount":100,"totalCosts":10,"positionExecutions":[]}`},
	})
	snapshot, found, err := v.c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: etoroTestCOID, Side: domain.OrderSideBuy})
	if err != nil || !found || snapshot.FilledQuantity != "0" || len(snapshot.Fills) != 0 || len(v.recorded()) != 1 {
		t.Fatalf("totals must not generate fills or an account-currency read: %+v found=%t err=%v", snapshot, found, err)
	}
}

func etoroCredentialSpellings(t *testing.T) []string {
	t.Helper()
	var credentials map[string]string
	if err := json.Unmarshal([]byte(etoroTestCredentials), &credentials); err != nil {
		t.Fatal(err)
	}
	var spellings []string
	for _, value := range credentials {
		spellings = append(spellings, value, url.PathEscape(value), url.QueryEscape(value))
		for _, escapeHTML := range []bool{true, false} {
			var buffer bytes.Buffer
			encoder := json.NewEncoder(&buffer)
			encoder.SetEscapeHTML(escapeHTML)
			if err := encoder.Encode(value); err != nil {
				t.Fatal(err)
			}
			spellings = append(spellings, buffer.String()[1:buffer.Len()-2])
		}
	}
	return spellings
}

func TestEtoroCredentialRedaction(t *testing.T) {
	spellings := etoroCredentialSpellings(t)
	text := strings.Join(spellings, " | ")
	encoded, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	assertRedacted := func(t *testing.T, value string) {
		t.Helper()
		for _, spelling := range spellings {
			if strings.Contains(value, spelling) {
				t.Fatal("credential spelling leaked in connector text")
			}
		}
	}
	for _, status := range []int{4, 10} {
		t.Run(fmt.Sprintf("reason-%d", status), func(t *testing.T) {
			lookup := strings.Replace(etoroTestLookup, `"status":{"id":3}`, fmt.Sprintf(`"status":{"id":%d,"errorMessage":%s}`, status, encoded), 1)
			v := newEtoroTestVenue(t, true, map[string]etoroTestReply{"/api/v2/trading/info/demo/orders:lookup": {200, lookup}})
			snapshot, _, err := v.c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: etoroTestCOID, Side: domain.OrderSideBuy})
			if err != nil || snapshot.Reason == "" || snapshot.Reason == text {
				t.Fatalf("rejection reason was lost or unredacted: err=%v", err)
			}
			assertRedacted(t, snapshot.Reason)
		})
	}
	t.Run("send-error", func(t *testing.T) {
		v := newEtoroTestVenue(t, true, map[string]etoroTestReply{"/api/v3/trading/execution/demo/orders": {400, `{"errorMessage":` + string(encoded) + `}`}})
		_, err := v.c.Send(context.Background(), etoroTestOrder())
		if !errors.Is(err, fwtrading.ErrRejected) {
			t.Fatalf("missing send refusal: %v", err)
		}
		assertRedacted(t, err.Error())
	})
	t.Run("unknown-action", func(t *testing.T) {
		lookup := strings.Replace(etoroTestLookup, `"action":"open"`, `"action":`+string(encoded), 1)
		v := newEtoroTestVenue(t, true, map[string]etoroTestReply{"/api/v2/trading/info/demo/orders:lookup": {200, lookup}})
		_, _, err := v.c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: etoroTestCOID, Side: domain.OrderSideBuy})
		if !errors.Is(err, domain.ErrUpstream) || len(v.diagnostics) == 0 {
			t.Fatalf("unknown action must error and diagnose: %v", err)
		}
		assertRedacted(t, err.Error())
		for _, diagnostic := range v.diagnostics {
			assertRedacted(t, fmt.Sprintf("%+v", diagnostic))
		}
	})
}

func TestEtoroCheckAccount(t *testing.T) {
	for _, demo := range []bool{true, false} {
		t.Run(fmt.Sprintf("demo=%t", demo), func(t *testing.T) {
			v := newEtoroTestVenue(t, demo, map[string]etoroTestReply{
				"/api/v1/me": {200, `{"gcid":7,"demoCid":9007199254740993,"realCid":9007199254740995,"username":"operator"}`},
			})
			want := "9007199254740995"
			if demo {
				want = "9007199254740993"
			}
			info, err := v.c.CheckAccount(context.Background())
			if err != nil || info != (fwtrading.AccountInfo{Identity: want}) {
				t.Fatalf("account = %+v, err = %v; want identity %s and empty status", info, err, want)
			}
			requests := v.recorded()
			if len(requests) != 1 || requests[0].method != http.MethodGet || requests[0].path != "/api/v1/me" || requests[0].query != "" {
				t.Fatalf("account requests = %+v", requests)
			}
		})
	}
}

func TestEtoroCheckAccountMissingCID(t *testing.T) {
	for _, demo := range []bool{true, false} {
		for _, cid := range []string{"missing", "null", "0", "-1", "1.5"} {
			t.Run(fmt.Sprintf("demo=%t/%s", demo, cid), func(t *testing.T) {
				field, other := "realCid", "demoCid"
				if demo {
					field, other = "demoCid", "realCid"
				}
				body := fmt.Sprintf(`{"gcid":7,"username":"operator","%s":99`, other)
				if cid != "missing" {
					body += fmt.Sprintf(`,"%s":%s`, field, cid)
				}
				v := newEtoroTestVenue(t, demo, map[string]etoroTestReply{"/api/v1/me": {200, body + "}"}})
				info, err := v.c.CheckAccount(context.Background())
				if !errors.Is(err, domain.ErrUpstream) || !strings.Contains(err.Error(), field) || info != (fwtrading.AccountInfo{}) {
					t.Fatalf("invalid %s must fail explicitly: account=%+v err=%v", field, info, err)
				}
			})
		}
	}
}

func TestEtoroVerifySymbol(t *testing.T) {
	for _, tc := range []struct {
		name, old, replacement string
		tradable               bool
	}{
		{name: "tradable", tradable: true},
		{name: "opening-disabled", old: `"allowOpenPosition":true`, replacement: `"allowOpenPosition":false`},
		{name: "cfd", old: `"settlementType":"real"`, replacement: `"settlementType":"cfd"`},
		{name: "short", old: `"direction":"long"`, replacement: `"direction":"short"`},
		{name: "leverage-two", old: `"leverageValues":[1]`, replacement: `"leverageValues":[2]`},
		{name: "potential", old: `"isPotential":false`, replacement: `"isPotential":true`},
		{name: "amount-only", old: `"allowedOrderQuantityType":"all"`, replacement: `"allowedOrderQuantityType":"amountOnly"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eligibility := etoroTestEligibility
			if tc.old != "" {
				eligibility = strings.Replace(eligibility, tc.old, tc.replacement, 1)
			}
			v := newEtoroTestVenue(t, true, map[string]etoroTestReply{
				"/api/v2/market-data/instruments":       {200, `{"results":[{"instrumentId":1001,"displayName":"Fake Apple","type":"Stocks","symbol":"AAPL","exchangeId":4}]}`},
				"/api/v2/trading/info/demo/eligibility": {200, eligibility},
			})
			got, err := v.c.VerifySymbol(context.Background(), "1001")
			want := fwtrading.SymbolVerification{Exists: true, Tradable: tc.tradable, Details: "Stocks AAPL Fake Apple"}
			if err != nil || got != want {
				t.Fatalf("verification = %+v, err = %v; want %+v", got, err, want)
			}
			requests := v.recorded()
			if len(requests) != 2 || requests[0].method != http.MethodGet || requests[0].query != "instrumentsIds=1001" || requests[1].method != http.MethodPost {
				t.Fatalf("verification requests = %+v", requests)
			}
			etoroAssertJSON(t, requests[1].body, `{"instrumentIds":[1001]}`)
		})
	}
}

func TestEtoroVerifySymbolNotFound(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply etoroTestReply
	}{
		{"empty", etoroTestReply{200, `{"results":[]}`}},
		{"different-id", etoroTestReply{200, `{"results":[{"instrumentId":1002,"symbol":"OTHER"}]}`}},
		{"http-404", etoroTestReply{404, `{"errorMessage":"No instrument found matching the provided criteria"}`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newEtoroTestVenue(t, true, map[string]etoroTestReply{"/api/v2/market-data/instruments": tc.reply})
			got, err := v.c.VerifySymbol(context.Background(), "1001")
			if err != nil || got != (fwtrading.SymbolVerification{}) {
				t.Fatalf("absent instrument = %+v, err = %v", got, err)
			}
			if requests := v.recorded(); len(requests) != 1 {
				t.Fatalf("absent instrument made %d requests, want only the catalogue request", len(requests))
			}
		})
	}
}

func TestEtoroVerifySymbolBadID(t *testing.T) {
	v := newEtoroTestVenue(t, true, nil)
	for _, id := range []string{"", "AAPL", "0", "-1", "01", "+1", "1.0", "1e3", " 1001", "1001 ", "1001&symbols=AAPL", "١"} {
		got, err := v.c.VerifySymbol(context.Background(), id)
		if err != nil || got != (fwtrading.SymbolVerification{}) {
			t.Fatalf("invalid id %q must be not found: verification=%+v err=%v", id, got, err)
		}
	}
	if requests := v.recorded(); len(requests) != 0 {
		t.Fatalf("invalid ids reached venue %d times", len(requests))
	}
}

func TestEtoroSearchSymbolsEmpty(t *testing.T) {
	v := newEtoroTestVenue(t, true, nil)
	for _, query := range []string{"", " \t\n", "\u2003"} {
		got, err := v.c.SearchSymbols(context.Background(), query)
		if err != nil || got == nil || len(got) != 0 {
			t.Fatalf("empty query must return an empty non-nil slice: matches=%+v err=%v", got, err)
		}
	}
	if requests := v.recorded(); len(requests) != 0 {
		t.Fatalf("empty query reached venue %d times", len(requests))
	}
}

func TestEtoroSearchSymbols(t *testing.T) {
	const catalogue = `{"results":[{"instrumentId":9007199254740993,"displayName":"Bitcoin / US Dollar","type":"Crypto","symbol":"BTC/USD","exchangeId":2},{"instrumentId":1001,"displayName":"Fake Apple","type":"Stocks","symbol":"AAPL","exchangeId":4}]}`
	var eligibility struct {
		Eligibilities []json.RawMessage `json:"eligibilities"`
	}
	if err := json.Unmarshal([]byte(etoroTestEligibility), &eligibility); err != nil {
		t.Fatal(err)
	}
	first := strings.Replace(string(eligibility.Eligibilities[0]), `"instrumentId":1001`, `"instrumentId":9007199254740993`, 1)
	first = strings.Replace(first, `"allowedOrderQuantityType":"all"`, `"allowedOrderQuantityType":"amountOnly"`, 1)
	response := `{"eligibilities":[` + string(eligibility.Eligibilities[0]) + "," + first + `],"notFoundInstrumentIds":[]}`
	for _, demo := range []bool{true, false} {
		t.Run(fmt.Sprintf("demo=%t", demo), func(t *testing.T) {
			v := newEtoroTestVenue(t, demo, map[string]etoroTestReply{
				"/api/v2/market-data/instruments/search":                    {200, catalogue},
				etoroTestPath(demo, "/api/v2/trading/info", "/eligibility"): {200, response},
			})
			got, err := v.c.SearchSymbols(context.Background(), " \tApple & BTC/USD\n")
			want := []fwtrading.SymbolMatch{
				{Symbol: "9007199254740993", Name: "BTC/USD - Bitcoin / US Dollar", Class: "Crypto", Exchange: "", BaseAsset: "", QuoteAsset: "", Tradable: false},
				{Symbol: "1001", Name: "AAPL - Fake Apple", Class: "Stocks", Exchange: "", BaseAsset: "", QuoteAsset: "", Tradable: true},
			}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("search mapping/order = %+v, err = %v; want %+v", got, err, want)
			}
			requests := v.recorded()
			if len(requests) != 2 || requests[0].method != http.MethodGet || requests[1].method != http.MethodPost {
				t.Fatalf("search must make one catalogue and one eligibility request: %+v", requests)
			}
			query, err := url.ParseQuery(requests[0].query)
			if err != nil || !reflect.DeepEqual(query, url.Values{"query": {"Apple & BTC/USD"}, "limit": {"50"}}) {
				t.Fatalf("search query = %v, err = %v", query, err)
			}
			etoroAssertJSON(t, requests[1].body, `{"instrumentIds":[9007199254740993,1001]}`)
		})
	}
}

func TestEtoroSearchSymbolsNoMatches(t *testing.T) {
	v := newEtoroTestVenue(t, true, map[string]etoroTestReply{
		"/api/v2/market-data/instruments/search": {200, `{"results":[]}`},
	})
	got, err := v.c.SearchSymbols(context.Background(), "nothing")
	if err != nil || got == nil || len(got) != 0 || len(v.recorded()) != 1 {
		t.Fatalf("no matches must return an empty non-nil slice without eligibility: matches=%+v err=%v requests=%d", got, err, len(v.recorded()))
	}
}

func TestEtoroSearchSymbolsOverlong(t *testing.T) {
	for _, query := range []string{strings.Repeat("a", 101), strings.Repeat("é", 101)} {
		v := newEtoroTestVenue(t, true, nil)
		got, err := v.c.SearchSymbols(context.Background(), " \t"+query+"\n")
		if !errors.Is(err, domain.ErrInvalid) || err.Error() != "eToro search query exceeds 100 characters: invalid" || got != nil || len(v.recorded()) != 0 {
			t.Fatalf("overlong query must be invalid without request: matches=%+v err=%v requests=%d", got, err, len(v.recorded()))
		}
	}
	for _, query := range []string{strings.Repeat("a", 100), strings.Repeat("é", 100)} {
		v := newEtoroTestVenue(t, true, map[string]etoroTestReply{
			"/api/v2/market-data/instruments/search": {200, `{"results":[]}`},
		})
		if _, err := v.c.SearchSymbols(context.Background(), query); err != nil {
			t.Fatalf("100-character query must be accepted: %v", err)
		}
		requests := v.recorded()
		if len(requests) != 1 {
			t.Fatalf("boundary query requests = %d, want 1", len(requests))
		}
		params, err := url.ParseQuery(requests[0].query)
		if err != nil || params.Get("query") != query {
			t.Fatal("boundary query was changed or truncated")
		}
	}
}

func TestEtoroCatalogueEligibilityNotFound(t *testing.T) {
	t.Run("verify", func(t *testing.T) {
		v := newEtoroTestVenue(t, true, map[string]etoroTestReply{
			"/api/v2/market-data/instruments":       {200, `{"results":[{"instrumentId":1001,"symbol":"AAPL","type":"Stocks","displayName":"Fake Apple"}]}`},
			"/api/v2/trading/info/demo/eligibility": {200, `{"notFoundInstrumentIds":[1001]}`},
		})
		got, err := v.c.VerifySymbol(context.Background(), "1001")
		want := fwtrading.SymbolVerification{Exists: true, Details: "Stocks AAPL Fake Apple"}
		if err != nil || got != want {
			t.Fatalf("eligibility-not-found verification = %+v, err = %v; want %+v", got, err, want)
		}
	})
	t.Run("search", func(t *testing.T) {
		response := strings.Replace(etoroTestEligibility, `"notFoundInstrumentIds":[]`, `"notFoundInstrumentIds":[1002]`, 1)
		v := newEtoroTestVenue(t, true, map[string]etoroTestReply{
			"/api/v2/market-data/instruments/search": {200, `{"results":[{"instrumentId":1002,"symbol":"MSFT","type":"Stocks","displayName":"Fake Microsoft"},{"instrumentId":1001,"symbol":"AAPL","type":"Stocks","displayName":"Fake Apple"}]}`},
			"/api/v2/trading/info/demo/eligibility":  {200, response},
		})
		got, err := v.c.SearchSymbols(context.Background(), "Fake")
		want := []fwtrading.SymbolMatch{
			{Symbol: "1002", Name: "MSFT - Fake Microsoft", Class: "Stocks", Tradable: false},
			{Symbol: "1001", Name: "AAPL - Fake Apple", Class: "Stocks", Tradable: true},
		}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("eligibility-not-found search = %+v, err = %v; want %+v", got, err, want)
		}
	})
}

func TestEtoroCatalogueFailures(t *testing.T) {
	for _, operation := range []string{"verify", "search"} {
		for _, tc := range []struct {
			name, path string
			reply      etoroTestReply
			wantErr    error
		}{
			{"catalogue-http", "catalogue", etoroTestReply{503, `{"errorMessage":"unavailable"}`}, nil},
			{"catalogue-unauthorized", "catalogue", etoroTestReply{401, `{"errorMessage":"unauthorized"}`}, nil},
			{"catalogue-decode", "catalogue", etoroTestReply{200, `{`}, io.ErrUnexpectedEOF},
			{"catalogue-missing-results", "catalogue", etoroTestReply{200, `{}`}, domain.ErrUpstream},
			{"catalogue-bad-id", "catalogue", etoroTestReply{200, `{"results":[{"instrumentId":0}]}`}, domain.ErrUpstream},
			{"catalogue-too-many", "catalogue", etoroTestReply{200, `{"results":[` + strings.TrimSuffix(strings.Repeat(`{"instrumentId":1001},`, 51), ",") + `]}`}, domain.ErrUpstream},
			{"eligibility-http", "eligibility", etoroTestReply{503, `{"errorMessage":"unavailable"}`}, nil},
			{"eligibility-404", "eligibility", etoroTestReply{404, `{"errorMessage":"not found"}`}, nil},
			{"eligibility-transport", "eligibility", etoroTestReply{-1, ``}, io.EOF},
			{"eligibility-decode", "eligibility", etoroTestReply{200, `{`}, io.ErrUnexpectedEOF},
			{"eligibility-absent", "eligibility", etoroTestReply{200, `{"eligibilities":[]}`}, fwtrading.ErrRejected},
			{"eligibility-missing-open", "eligibility", etoroTestReply{200, `{"eligibilities":[{"instrumentId":1001}]}`}, domain.ErrUpstream},
			{"eligibility-invalid-config", "eligibility", etoroTestReply{200, strings.Replace(etoroTestEligibility, `"direction":"long"`, `"direction":"unknown"`, 1)}, domain.ErrUpstream},
		} {
			if tc.name == "catalogue-too-many" && operation == "verify" {
				continue
			}
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				path := "/api/v2/market-data/instruments"
				if operation == "search" {
					path += "/search"
				}
				replies := map[string]etoroTestReply{path: {200, `{"results":[{"instrumentId":1001,"symbol":"AAPL","type":"Stocks","displayName":"Fake Apple"}]}`}}
				if tc.path == "eligibility" {
					path = "/api/v2/trading/info/demo/eligibility"
				}
				replies[path] = tc.reply
				v := newEtoroTestVenue(t, true, replies)
				var err error
				if operation == "search" {
					var matches []fwtrading.SymbolMatch
					matches, err = v.c.SearchSymbols(context.Background(), "Apple")
					if matches != nil {
						t.Fatal("failed search returned a silently untradable list")
					}
				} else {
					_, err = v.c.VerifySymbol(context.Background(), "1001")
				}
				if err == nil {
					t.Fatal("catalogue/eligibility failure must fail the operation")
				}
				if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
					t.Fatalf("catalogue/eligibility: got %v, want %v", err, tc.wantErr)
				}
				if tc.reply.status >= 400 {
					var apiErr *etoro.APIError
					if !errors.As(err, &apiErr) || apiErr.Status != tc.reply.status {
						t.Fatalf("catalogue/eligibility: got %v, want an HTTP %d *etoro.APIError", err, tc.reply.status)
					}
				}
			})
		}
	}
}

func TestNewEtoroConnector(t *testing.T) {
	for _, mode := range []domain.TradingMode{domain.TradingModePaper, domain.TradingModeLive} {
		t.Run(string(mode), func(t *testing.T) {
			built, err := NewEtoroConnector(domain.TradingConnection{Mode: mode, Credentials: etoroTestCredentials})
			if err != nil {
				t.Fatal(err)
			}
			c, ok := built.(*etoroConnector)
			if !ok || c.demo != (mode == domain.TradingModePaper) {
				t.Fatalf("constructor selected wrong account for %s", mode)
			}
			v := newEtoroTestVenue(t, mode == domain.TradingModePaper, nil)
			c.client = v.c.client
			if err := c.PreCheck(context.Background(), etoroTestOrder()); err != nil {
				t.Fatalf("constructor selected wrong trading path: %v", err)
			}
			requests := v.recorded()
			want := etoroTestPath(mode == domain.TradingModePaper, "/api/v2/trading/info", "/eligibility")
			if len(requests) != 1 || requests[0].path != want {
				t.Fatalf("constructor paths = %+v, want %s", requests, want)
			}
		})
	}
	for _, mode := range []domain.TradingMode{"", "unknown"} {
		if c, err := NewEtoroConnector(domain.TradingConnection{Mode: mode, Credentials: etoroTestCredentials}); err == nil || c != nil {
			t.Fatalf("unknown mode must fail explicitly: connector returned=%t err=%v", c != nil, err)
		}
	}
}

func TestEtoroProvider(t *testing.T) {
	p := EtoroProvider()
	if p.Type != "etoro" || p.Title != "eToro" || p.VenueAccounts || !p.VerifiesSymbols || !p.SearchesSymbols || p.UnlinkedFees {
		t.Fatalf("eToro provider flags = %+v", p)
	}
	c, err := p.Build(domain.TradingConnection{Mode: domain.TradingModePaper, Credentials: etoroTestCredentials})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.(*etoroConnector); !ok {
		t.Fatal("eToro provider did not build its connector")
	}
}

func TestEtoroCatalogueCredentialRedaction(t *testing.T) {
	spellings := etoroCredentialSpellings(t)
	payload, err := json.Marshal(map[string]string{"errorMessage": strings.Join(spellings, " | ")})
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"account", "verify", "verify-eligibility", "search", "search-eligibility"} {
		t.Run(operation, func(t *testing.T) {
			path := "/api/v1/me"
			switch operation {
			case "verify":
				path = "/api/v2/market-data/instruments"
			case "search":
				path = "/api/v2/market-data/instruments/search"
			case "verify-eligibility", "search-eligibility":
				path = "/api/v2/trading/info/demo/eligibility"
			}
			replies := map[string]etoroTestReply{
				"/api/v2/market-data/instruments":        {200, `{"results":[{"instrumentId":1001}]}`},
				"/api/v2/market-data/instruments/search": {200, `{"results":[{"instrumentId":1001}]}`},
			}
			replies[path] = etoroTestReply{401, string(payload)}
			v := newEtoroTestVenue(t, true, replies)
			switch operation {
			case "account":
				_, err = v.c.CheckAccount(context.Background())
			case "verify", "verify-eligibility":
				_, err = v.c.VerifySymbol(context.Background(), "1001")
			case "search", "search-eligibility":
				_, err = v.c.SearchSymbols(context.Background(), "Apple")
			}
			var apiErr *etoro.APIError
			if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
				t.Fatalf("upstream refusal: got %v, want an HTTP 401 *etoro.APIError", err)
			}
			for _, spelling := range spellings {
				if strings.Contains(err.Error(), spelling) {
					t.Fatal("credential spelling leaked in catalogue/account error")
				}
			}
		})
	}
}
