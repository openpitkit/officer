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
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/framework/trading"
)

const tradingSecret = "k8-known-credential-value"
const tradingPath = "/trading/connections/connection-1"
const freshTradingJSON = `{"id":"connection-1","provider":"alpaca","label":"Paper","mode":"paper","enabled":false,"venueIdentity":"venue-1","feeAccount":"","state":"stopped","secrets":{"key":true},"instruments":[],"access":[]}`

func tradingFixture(t *testing.T) *fakeService {
	t.Helper()
	id, err := domain.ParseExternalID("connection-1")
	if err != nil {
		t.Fatal(err)
	}
	return &fakeService{
		tradingStatus: backend.TradingStatus{Connections: []backend.TradingConnectionStatus{{
			ExternalID: id, Provider: "alpaca", Label: "Paper", Mode: domain.TradingModePaper,
			VenueIdentity: "venue-1", State: "stopped", Secrets: map[string]bool{"key": true},
		}}},
		tradingCreateResult: domain.TradingConnection{ExternalID: id, Credentials: tradingSecret},
		tradingCheck:        trading.AccountInfo{Identity: "venue-1", Status: "ACTIVE"},
		tradingVerification: backend.TradingSymbolVerification{Supported: true, Exists: true, Tradable: true},
		tradingSearch:       backend.TradingSymbolSearch{Supported: true, Matches: []trading.SymbolMatch{{Symbol: "AAPL", Tradable: true}}},
		tradingDestinations: []backend.TradingDestinationStatus{{Connection: id, Label: "Paper", Provider: "alpaca", Mode: domain.TradingModePaper, State: "stopped"}},
	}
}

func callTrading(t *testing.T, svc *fakeService, mount, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	router, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(method, mount+path, strings.NewReader(body)))
	if strings.Contains(rec.Body.String(), tradingSecret) {
		t.Fatal("response leaked credentials")
	}
	return rec
}

func assertTradingJSON(t *testing.T, rec *httptest.ResponseRecorder, status int, expected string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status=%d, want %d: %s", rec.Code, status, rec.Body)
	}
	if expected == "" {
		if rec.Body.Len() != 0 {
			t.Fatalf("unexpected body: %s", rec.Body)
		}
		return
	}
	var got, want any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(expected), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("body=%s, want %s", rec.Body, expected)
	}
}

func TestTradingRoutesSuccess(t *testing.T) {
	snapshot := `{"trading":{"providers":[],"connections":[` + freshTradingJSON + `],"credentialsSealed":false}}`
	for _, tc := range []struct {
		name, method, path, body, response, call string
		status                                   int
	}{
		{"get", "GET", "/trading", "", snapshot, "get", 200},
		{"create", "POST", "/trading/connections", `{"id":"connection-1","provider":"alpaca","label":"Paper","mode":"paper","credentials":{ "key": "` + tradingSecret + `" },"enabled":true}`, `{"connection":` + freshTradingJSON + `}`, "create", 201},
		{"settings", "PUT", tradingPath + "/settings", `{"label":"Renamed","credentials":{ "key": "` + tradingSecret + `" }}`, snapshot, "settings", 200},
		{"enabled", "PUT", tradingPath + "/enabled", `{"enabled":true}`, `{"enabled":true}`, "enabled", 200},
		{"check", "POST", tradingPath + "/check", "", `{"check":{"venueIdentity":"venue-1","venueStatus":"ACTIVE"}}`, "check", 200},
		{"instrument", "PUT", tradingPath + "/instruments", `{"externalSymbol":"AAPL","baseAsset":"AAPL","quoteAsset":"USD","enabled":true}`, snapshot, "instrument", 200},
		{"delete-instrument", "DELETE", tradingPath + "/instruments?externalSymbol=AAPL", "", "", "delete-instrument", 204},
		{"verify", "POST", tradingPath + "/verify-symbol", `{"externalSymbol":"AAPL"}`, `{"verification":{"supported":true,"exists":true,"tradable":true}}`, "verify", 200},
		{"search", "POST", tradingPath + "/search-symbols", `{"query":"apple"}`, `{"supported":true,"matches":[{"symbol":"AAPL","tradable":true}]}`, "search", 200},
		{"access", "PUT", tradingPath + "/access", `{"account":"acc-1","venueAccount":"sub-1"}`, snapshot, "access", 200},
		{"access-group", "PUT", tradingPath + "/access", `{"accountGroup":"desk","venueAccount":"sub-1"}`, snapshot, "access", 200},
		{"delete-access", "DELETE", tradingPath + "/access?account=acc-1", "", "", "delete-access", 204},
		{"delete-access-group", "DELETE", tradingPath + "/access?accountGroup=desk", "", "", "delete-access", 204},
		{"fee-account", "PUT", tradingPath + "/fee-account", `{"account":"acc-1"}`, snapshot, "fee-account", 200},
		{"delete-fee-account", "DELETE", tradingPath + "/fee-account", "", "", "fee-account", 204},
		{"destinations", "GET", "/accounts/acc-1/trading-destinations?mode=paper", "", `{"destinations":[{"connection":"connection-1","label":"Paper","provider":"alpaca","mode":"paper","enabled":false,"venueAccount":"","state":"stopped"}]}`, "destinations", 200},
	} {
		for _, mount := range []string{"/api/v1", "/app/api/v1"} {
			t.Run(tc.name+mount, func(t *testing.T) {
				svc := tradingFixture(t)
				assertTradingJSON(t, callTrading(t, svc, mount, tc.method, tc.path, tc.body), tc.status, tc.response)
				if len(svc.tradingCalls) == 0 || svc.tradingCalls[0] != tc.call {
					t.Fatalf("calls=%v", svc.tradingCalls)
				}
				switch tc.name {
				case "create":
					c := svc.tradingConnection
					if c.ExternalID.String() != "connection-1" || c.Provider != "alpaca" || c.Label != "Paper" || c.Mode != domain.TradingModePaper || !c.Enabled || c.Credentials != `{"key":"`+tradingSecret+`"}` {
						t.Fatal("create input differs from request")
					}
				case "settings":
					if svc.tradingLabel != "Renamed" || svc.tradingCredentials != `{"key":"`+tradingSecret+`"}` {
						t.Fatal("settings input differs from request")
					}
				case "instrument":
					if svc.tradingInstrument != (domain.TradingInstrument{Connection: svc.tradingCreateResult.ExternalID, ExternalSymbol: "AAPL", BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true}) {
						t.Fatal("instrument input differs from request")
					}
				case "enabled":
					if !svc.tradingEnabled {
						t.Fatal("enabled input differs from request")
					}
				case "access":
					if svc.tradingAccess != (domain.TradingAccess{Connection: svc.tradingCreateResult.ExternalID, Account: "acc-1", VenueAccount: "sub-1"}) {
						t.Fatal("access input differs from request")
					}
				case "access-group":
					if svc.tradingAccess != (domain.TradingAccess{Connection: svc.tradingCreateResult.ExternalID, AccountGroup: "desk", VenueAccount: "sub-1"}) {
						t.Fatalf("group access input = %+v, differs from request", svc.tradingAccess)
					}
				case "delete-access-group":
					if svc.tradingAccount != "" || svc.tradingAccountGroup != "desk" {
						t.Fatalf("revoked subject = %q/%q, want group desk", svc.tradingAccount, svc.tradingAccountGroup)
					}
				case "delete-access", "fee-account", "destinations":
					if svc.tradingAccount != "acc-1" || svc.tradingAccountGroup != "" {
						t.Fatal("account was not forwarded")
					}
				case "delete-fee-account":
					if svc.tradingAccount != "" {
						t.Fatal("fee account was not cleared")
					}
				}
				if tc.name == "search" && svc.tradingQuery != "apple" {
					t.Fatal("query was not forwarded")
				}
				if (tc.name == "verify" || tc.name == "delete-instrument") && svc.tradingSymbol != "AAPL" {
					t.Fatal("symbol was not forwarded")
				}
				if tc.name == "destinations" && svc.tradingMode != domain.TradingModePaper {
					t.Fatal("mode was not forwarded")
				}
				if tc.name != "get" && tc.name != "create" && tc.name != "instrument" && tc.name != "access" && tc.name != "access-group" && tc.name != "destinations" && svc.tradingID != "connection-1" {
					t.Fatal("connection id was not forwarded")
				}
			})
		}
	}
}

func TestTradingRequiredFields(t *testing.T) {
	create := `{"provider":"alpaca","label":"Paper","mode":"paper","credentials":{"key":"` + tradingSecret + `"},"enabled":false}`
	instrument := `{"externalSymbol":"AAPL","baseAsset":"AAPL","quoteAsset":"USD","enabled":false}`
	for _, tc := range []struct{ method, path, body, field string }{
		{"POST", "/trading/connections", create, "label"},
		{"POST", "/trading/connections", create, "enabled"},
		{"POST", "/trading/connections", create, "provider"},
		{"POST", "/trading/connections", create, "mode"},
		{"POST", "/trading/connections", create, "credentials"},
		{"PUT", tradingPath + "/enabled", `{"enabled":true}`, "enabled"},
		{"PUT", tradingPath + "/instruments", instrument, "externalSymbol"},
		{"PUT", tradingPath + "/instruments", instrument, "baseAsset"},
		{"PUT", tradingPath + "/instruments", instrument, "quoteAsset"},
		{"PUT", tradingPath + "/instruments", instrument, "enabled"},
		{"POST", tradingPath + "/verify-symbol", `{"externalSymbol":"AAPL"}`, "externalSymbol"},
		{"POST", tradingPath + "/search-symbols", `{"query":"apple"}`, "query"},
		{"PUT", tradingPath + "/access", `{"account":"acc-1"}`, "account"},
		{"PUT", tradingPath + "/fee-account", `{"account":"acc-1"}`, "account"},
		{"DELETE", tradingPath + "/instruments", "", "externalSymbol"},
		{"DELETE", tradingPath + "/access", "", "account"},
	} {
		for _, value := range []string{"omitted", "null", "blank"} {
			if tc.method == "DELETE" && value != "omitted" {
				continue
			}
			if (tc.field == "credentials" || tc.field == "enabled") && value == "blank" {
				continue
			}
			t.Run(tc.method+tc.path+"/"+tc.field+"/"+value, func(t *testing.T) {
				body := tc.body
				if body != "" {
					var fields map[string]any
					if err := json.Unmarshal([]byte(body), &fields); err != nil {
						t.Fatal(err)
					}
					delete(fields, tc.field)
					if value == "null" {
						fields[tc.field] = nil
					}
					if value == "blank" {
						fields[tc.field] = "  "
					}
					encoded, err := json.Marshal(fields)
					if err != nil {
						t.Fatal(err)
					}
					body = string(encoded)
				}
				svc := tradingFixture(t)
				rec := callTrading(t, svc, "/api/v1", tc.method, tc.path, body)
				if rec.Code != 422 {
					t.Fatalf("status=%d, want 422: %s", rec.Code, rec.Body)
				}
				m := bodyMap(t, rec.Result())
				errs, ok := m["errors"].([]any)
				if !ok || len(errs) != 1 || errs[0].(map[string]any)["pointer"] != "/"+tc.field {
					t.Fatalf("wrong validation field: %v", m)
				}
				if len(svc.tradingCalls) != 0 {
					t.Fatalf("invalid request reached service: %v", svc.tradingCalls)
				}
			})
		}
	}
}

// TestTradingAccessSubject pins the grant subject rules of PUT and DELETE
// access: naming neither subject is refused at /account before the service is
// called, and naming both reaches the service whole, whose domain validation
// error - set on the fake here - answers at /accountGroup.
func TestTradingAccessSubject(t *testing.T) {
	id, err := domain.ParseExternalID("connection-1")
	if err != nil {
		t.Fatal(err)
	}
	both := domain.ValidateTradingAccess(domain.TradingAccess{Account: "acc-1", AccountGroup: "desk", Connection: id})
	for _, tc := range []struct {
		name, method, path, body, pointer, constraint string
		serviceErr                                    error
	}{
		{"put-neither", "PUT", tradingPath + "/access", `{"account":" ","accountGroup":"  "}`, "/account", "required", nil},
		{"delete-neither", "DELETE", tradingPath + "/access?account=&accountGroup=%20", "", "/account", "required", nil},
		{"put-both", "PUT", tradingPath + "/access", `{"account":"acc-1","accountGroup":"desk"}`, "/accountGroup", "unsupported_value", both},
		{"delete-both", "DELETE", tradingPath + "/access?account=acc-1&accountGroup=desk", "", "/accountGroup", "unsupported_value", both},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := tradingFixture(t)
			svc.tradingErr = tc.serviceErr
			rec := callTrading(t, svc, "/api/v1", tc.method, tc.path, tc.body)
			if rec.Code != 422 {
				t.Fatalf("status=%d, want 422: %s", rec.Code, rec.Body)
			}
			problems, _ := bodyMap(t, rec.Result())["errors"].([]any)
			if len(problems) != 1 {
				t.Fatalf("problems = %v, want one", problems)
			}
			if problem := problems[0].(map[string]any); problem["pointer"] != tc.pointer || problem["constraint"] != tc.constraint {
				t.Fatalf("problem = %v, want %s at %s", problem, tc.constraint, tc.pointer)
			}
			switch {
			case tc.serviceErr == nil:
				if len(svc.tradingCalls) != 0 {
					t.Fatalf("request without a subject reached the service: %v", svc.tradingCalls)
				}
			case tc.method == "PUT":
				if svc.tradingAccess.Account != "acc-1" || svc.tradingAccess.AccountGroup != "desk" {
					t.Fatalf("service got %+v, want both subjects", svc.tradingAccess)
				}
			default:
				if svc.tradingAccount != "acc-1" || svc.tradingAccountGroup != "desk" {
					t.Fatalf("service got %q/%q, want both subjects", svc.tradingAccount, svc.tradingAccountGroup)
				}
			}
		})
	}
}

func TestTradingCredentialsObject(t *testing.T) {
	for _, raw := range []string{`"` + tradingSecret + `"`, `[]`, `42`, `null`} {
		for _, path := range []string{"/trading/connections", tradingPath + "/settings"} {
			t.Run(path+raw, func(t *testing.T) {
				method, body := "PUT", `{"credentials":`+raw+`}`
				if path == "/trading/connections" {
					method = "POST"
					body = `{"provider":"alpaca","label":"Paper","mode":"paper","enabled":true,"credentials":` + raw + `}`
				}
				svc := tradingFixture(t)
				rec := callTrading(t, svc, "/api/v1", method, path, body)
				if rec.Code != 422 || len(svc.tradingCalls) != 0 {
					t.Fatalf("status=%d calls=%v: %s", rec.Code, svc.tradingCalls, rec.Body)
				}
			})
		}
	}
	for _, body := range []string{`{}`, `{"label":"Renamed"}`} {
		svc := tradingFixture(t)
		rec := callTrading(t, svc, "/api/v1", "PUT", tradingPath+"/settings", body)
		if rec.Code != 200 || svc.tradingCredentials != "" {
			t.Fatal("omitted credentials must remain omitted")
		}
	}
	for _, label := range []string{"null", `""`, `"  "`, "42"} {
		svc := tradingFixture(t)
		rec := callTrading(t, svc, "/api/v1", "PUT", tradingPath+"/settings", `{"label":`+label+`}`)
		if rec.Code != 422 || len(svc.tradingCalls) != 0 {
			t.Fatalf("invalid settings label accepted: status=%d calls=%v", rec.Code, svc.tradingCalls)
		}
	}
}

func TestTradingOptionalFieldsAndEmptyCollections(t *testing.T) {
	svc := &fakeService{}
	assertTradingJSON(t, callTrading(t, svc, "/api/v1", "GET", "/trading", ""), 200, `{"trading":{"providers":[],"connections":[],"credentialsSealed":false}}`)
	assertTradingJSON(t, callTrading(t, svc, "/api/v1", "GET", "/accounts/acc-1/trading-destinations", ""), 200, `{"destinations":[]}`)
	assertTradingJSON(t, callTrading(t, svc, "/api/v1", "POST", tradingPath+"/search-symbols", `{"query":"x"}`), 200, `{"supported":false,"matches":[]}`)
	assertTradingJSON(t, callTrading(t, svc, "/api/v1", "POST", tradingPath+"/verify-symbol", `{"externalSymbol":"x"}`), 200, `{"verification":{"supported":false,"exists":false,"tradable":false}}`)
	svc = tradingFixture(t)
	svc.tradingStatus.Providers = []backend.TradingProvider{{Type: "alpaca", Title: "Alpaca", VenueAccounts: true, VerifiesSymbols: true, SearchesSymbols: true, UnlinkedFees: true}}
	svc.tradingStatus.CredentialsSealed = true
	c := &svc.tradingStatus.Connections[0]
	c.State = "unavailable"
	c.Unavailable = "keys unavailable"
	c.FeeAccount = "fees"
	c.Instruments = []domain.TradingInstrument{{ExternalSymbol: "AAPL", BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true}}
	c.Access = []store.TradingAccessListRow{
		{Access: domain.TradingAccess{AccountGroup: "desk", VenueAccount: "sub-g"}},
		{Access: domain.TradingAccess{Account: "acc-1", VenueAccount: "sub-1"}, Group: "desk"},
		{Access: domain.TradingAccess{Account: "acc-2"}},
	}
	rec := callTrading(t, svc, "/api/v1", "GET", "/trading", "")
	expected := strings.Replace(freshTradingJSON, `"feeAccount":"","state":"stopped"`, `"feeAccount":"fees","state":"unavailable","error":"keys unavailable"`, 1)
	expected = strings.Replace(expected, `"instruments":[],"access":[]`, `"instruments":[{"externalSymbol":"AAPL","baseAsset":"AAPL","quoteAsset":"USD","enabled":true}],"access":[`+
		`{"account":"","accountGroup":"desk","venueAccount":"sub-g","group":""},`+
		`{"account":"acc-1","accountGroup":"","venueAccount":"sub-1","group":"desk"},`+
		`{"account":"acc-2","accountGroup":"","venueAccount":"","group":""}]`, 1)
	assertTradingJSON(t, rec, 200, `{"trading":{"providers":[{"type":"alpaca","title":"Alpaca","venueAccounts":true,"verifiesSymbols":true,"searchesSymbols":true,"unlinkedFees":true}],"connections":[`+expected+`],"credentialsSealed":true}}`)
	svc.tradingDestinations[0].State = "unavailable"
	svc.tradingDestinations[0].Unavailable = "keys unavailable"
	assertTradingJSON(t, callTrading(t, svc, "/api/v1", "GET", "/accounts/acc-1/trading-destinations?mode=live", ""), 200, `{"destinations":[{"connection":"connection-1","label":"Paper","provider":"alpaca","mode":"paper","enabled":false,"venueAccount":"","state":"unavailable","error":"keys unavailable"}]}`)
	svc.tradingVerification.Details = "listed"
	assertTradingJSON(t, callTrading(t, svc, "/api/v1", "POST", tradingPath+"/verify-symbol", `{"externalSymbol":"AAPL"}`), 200, `{"verification":{"supported":true,"exists":true,"tradable":true,"details":"listed"}}`)
	svc.tradingSearch.Matches[0] = trading.SymbolMatch{Symbol: "AAPL", Name: "Apple", Class: "equity", Exchange: "NASDAQ", Tradable: true}
	assertTradingJSON(t, callTrading(t, svc, "/api/v1", "POST", tradingPath+"/search-symbols", `{"query":"apple"}`), 200, `{"supported":true,"matches":[{"symbol":"AAPL","name":"Apple","class":"equity","exchange":"NASDAQ","tradable":true}]}`)
	c.State = "running"
	if strings.Contains(callTrading(t, svc, "/api/v1", "GET", "/trading", "").Body.String(), `"error"`) {
		t.Fatal("running connection exposes unavailable error")
	}
}

func TestTradingSnapshotErrors(t *testing.T) {
	for _, mount := range []string{"/api/v1", "/app/api/v1"} {
		t.Run("mutation"+mount, func(t *testing.T) {
			svc := tradingFixture(t)
			svc.tradingSnapshotErr = fmt.Errorf("snapshot failed: %w", domain.ErrUpstream)
			rec := callTrading(t, svc, mount, "PUT", tradingPath+"/settings", `{"label":"Renamed"}`)
			assertTradingJSON(t, rec, 502, `{"error":{"code":"upstream","message":"The provider couldn't complete the request. Check your provider access and the request, then try again."}}`)
			if strings.Join(svc.tradingCalls, ",") != "settings,get" || svc.tradingLabel != "Renamed" {
				t.Fatalf("mutation did not precede the snapshot: calls=%v", svc.tradingCalls)
			}
		})
		t.Run("created-id-missing"+mount, func(t *testing.T) {
			svc := tradingFixture(t)
			otherID, err := domain.ParseExternalID("other-connection")
			if err != nil {
				t.Fatal(err)
			}
			svc.tradingStatus.Connections[0].ExternalID = otherID
			rec := callTrading(t, svc, mount, "POST", "/trading/connections", `{"provider":"alpaca","label":"Paper","mode":"paper","credentials":{"key":"`+tradingSecret+`"},"enabled":true}`)
			assertTradingJSON(t, rec, 500, `{"error":{"code":"internal","message":"internal error"}}`)
			if strings.Join(svc.tradingCalls, ",") != "create,get" {
				t.Fatalf("calls=%v", svc.tradingCalls)
			}
		})
	}
}

func TestTradingHandlerErrors(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body, code string
		err                            error
		status                         int
	}{
		{"live", "POST", "/trading/connections", `{"provider":"alpaca","label":"Live","mode":"live","credentials":{"key":"` + tradingSecret + `"},"enabled":true}`, "live_trading_unavailable", domain.ErrLiveTradingUnavailable, 403},
		{"fee", "PUT", tradingPath + "/access", `{"account":"acc-1"}`, "fee_account_required", domain.ErrFeeAccountRequired, 409},
		{"fee-group", "PUT", tradingPath + "/access", `{"accountGroup":"desk"}`, "fee_account_required", domain.ErrFeeAccountRequired, 409},
		{"report", "POST", "/orders/order-1/execution-reports", `{"status":"cancelled"}`, "venue_order_managed", domain.ErrVenueOrderManaged, 409},
		{"confirm", "POST", "/orders/order-1/confirm", "", "venue_order_managed", domain.ErrVenueOrderManaged, 409},
		{"cancel", "POST", "/orders/order-1/cancel", `{}`, "venue_order_managed", domain.ErrVenueOrderManaged, 409},
		{"upstream", "POST", tradingPath + "/check", "", "upstream", domain.ErrUpstream, 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := tradingFixture(t)
			svc.tradingErr = fmt.Errorf("operation failed: %w", tc.err)
			svc.execReportErr = tc.err
			svc.confirmErr = tc.err
			svc.cancelErr = tc.err
			rec := callTrading(t, svc, "/api/v1", tc.method, tc.path, tc.body)
			if rec.Code != tc.status {
				t.Fatalf("status=%d want=%d: %s", rec.Code, tc.status, rec.Body)
			}
			if bodyMap(t, rec.Result())["error"].(map[string]any)["code"] != tc.code {
				t.Fatalf("wrong error code: %s", rec.Body)
			}
		})
	}
	for _, tc := range []struct{ path, body string }{
		{tradingPath + "/search-symbols", `{"query":"  "}`},
		{tradingPath + "/search-symbols", `{"query":"\t\n"}`},
	} {
		svc := tradingFixture(t)
		rec := callTrading(t, svc, "/api/v1", "POST", tc.path, tc.body)
		if rec.Code != 422 || len(svc.tradingCalls) != 0 {
			t.Fatal("blank query reached service")
		}
	}
	svc := tradingFixture(t)
	if rec := callTrading(t, svc, "/api/v1", "GET", "/accounts/acc-1/trading-destinations?mode=invalid", ""); rec.Code != 422 || len(svc.tradingCalls) != 0 {
		t.Fatal("invalid mode reached service")
	}
}

func TestOrderVenueLinkShape(t *testing.T) {
	svc := tradingFixture(t)
	rec := callTrading(t, svc, "/api/v1", "GET", "/orders/order-1", "")
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if _, exists := bodyMap(t, rec.Result())["venueOrder"]; exists {
		t.Fatal("ordinary order has venueOrder")
	}
	svc.orderDetail.Venue = &domain.VenueOrder{Connection: svc.tradingCreateResult.ExternalID, Route: `{"limit":123,"nested":{"flag":true}}`, ClientOrderID: "client-1", CreatedAt: time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)}
	for _, ack := range []bool{false, true} {
		svc.orderDetail.Venue.SendAttempted = ack
		if ack {
			svc.orderDetail.Venue.VenueOrderID = "venue-order-1"
		}
		rec := callTrading(t, svc, "/api/v1", "GET", "/orders/order-1", "")
		if rec.Code != 200 {
			t.Fatalf("status=%d: %s", rec.Code, rec.Body)
		}
		m := bodyMap(t, rec.Result())
		encoded, err := json.Marshal(m["venueOrder"])
		if err != nil {
			t.Fatal(err)
		}
		expected := `{"connection":"connection-1","venueAccount":"","route":{"limit":123,"nested":{"flag":true}},"clientOrderId":"client-1","sendAttempted":false,"createdAt":"2026-10-05T00:00:00Z"}`
		if ack {
			expected = strings.Replace(expected, `"sendAttempted":false`, `"sendAttempted":true,"venueOrderId":"venue-order-1"`, 1)
		}
		result := httptest.NewRecorder()
		result.WriteHeader(200)
		_, _ = result.Write(encoded)
		assertTradingJSON(t, result, 200, expected)
	}
	svc.orderDetail.Venue.Route = `"invalid"`
	if rec := callTrading(t, svc, "/api/v1", "GET", "/orders/order-1", ""); rec.Code != 500 {
		t.Fatal("invalid stored route was accepted")
	}
}

func TestSendPaperOrderResponse(t *testing.T) {
	for _, status := range []string{"acknowledged", "refused", "unknown", "reject"} {
		t.Run(status, func(t *testing.T) {
			svc := &fakeService{paperSendResult: backend.VenueSendResult{
				Token: backend.ApprovalToken{Token: "approval", KeyID: "key",
					OrderExternalID: "order", Verdict: "accept"}}}
			want := `{"signedApproval":"approval","keyId":"key","id":"order","verdict":"accept"`
			if status == "reject" {
				svc.paperSendResult.Token.Verdict = "reject"
				want = strings.Replace(want, `"accept"`, `"reject"`, 1) + `}`
			} else {
				svc.paperSendResult.Send = &backend.VenueSend{Status: status}
				want += `,"venueSend":{"status":"` + status + `"`
				if status == "acknowledged" {
					svc.paperSendResult.Send.Order = &domain.VenueOrder{Connection: "conn", Route: `{"timeInForce":"day"}`,
						ClientOrderID: "client", VenueOrderID: "venue", SendAttempted: true, CreatedAt: time.Unix(0, 0).UTC()}
					want += `,"venueOrder":{"connection":"conn","venueAccount":"","route":{"timeInForce":"day"},"clientOrderId":"client","venueOrderId":"venue","sendAttempted":true,"createdAt":"1970-01-01T00:00:00Z"}`
				} else {
					svc.paperSendResult.Send.Error = "venue reason"
					want += `,"error":"venue reason"`
				}
				want += `}}`
			}
			rec := callTrading(t, svc, "/api/v1", "POST", "/orders/send-paper",
				`{"id":"order","account":"desk","baseAsset":"AAPL","quoteAsset":"USD","side":"buy","amountKind":"quantity","amountValue":"10","price":"2","destination":{"connection":"conn","venueAccount":"remote","route":{ "timeInForce": "day" }}}`)
			assertTradingJSON(t, rec, 201, want)
			if svc.paperDestination != (backend.SendDestination{Connection: "conn", VenueAccount: "remote", Route: `{"timeInForce":"day"}`}) ||
				svc.submitOrderIn.ExternalID != "order" || svc.submitOrderIn.Account != "desk" || svc.submitOrderIn.Price != "2" {
				t.Fatal("paper send input was not delegated")
			}
		})
	}
}

func TestSendPaperOrderErrors(t *testing.T) {
	for _, tc := range []struct{ route, code string }{
		{"", "required"}, {"null", "type"}, {"[]", "type"},
		{`"{}"`, "type"}, {"42", "type"}, {"true", "type"},
	} {
		t.Run("route/"+tc.route, func(t *testing.T) {
			destination := "{}"
			if tc.route != "" {
				destination = `{"route":` + tc.route + `}`
			}
			svc := &fakeService{}
			rec := callTrading(t, svc, "/api/v1", "POST", "/orders/send-paper",
				`{"account":"desk","destination":`+destination+`}`)
			body := bodyMap(t, rec.Result())
			problems, ok := body["errors"].([]any)
			if rec.Code != 422 || !ok || len(problems) != 1 {
				t.Fatalf("route validation = %d %v", rec.Code, body)
			}
			problem := problems[0].(map[string]any)
			if problem["pointer"] != "/destination/route" || problem["constraint"] != tc.code ||
				svc.paperDestination.Route != "" {
				t.Fatalf("route problem = %v, destination = %+v", problem, svc.paperDestination)
			}
		})
	}
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{domain.ErrTradingModeMismatch, 409, "trading_mode_mismatch"},
		{domain.ErrNoDestination, 409, "no_destination"},
		{&backend.DestinationAmbiguousError{Candidates: []backend.TradingDestinationStatus{{Connection: "conn", Label: "Paper"}}}, 409, "destination_ambiguous"},
		{domain.ErrForbidden, 403, "forbidden"}, {domain.ErrNotFound, 404, "not_found"},
		{fmt.Errorf("venue rule refused: %w", domain.ErrInvalid), 422, "validation"},
		{domain.ErrUpstream, 502, "upstream"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			svc := &fakeService{tradingErr: tc.err}
			rec := callTrading(t, svc, "/api/v1", "POST", "/orders/send-paper",
				`{"account":"desk","destination":{"route":{}}}`)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), `"code":"`+tc.code+`"`) {
				t.Fatalf("paper send error = %d %s", rec.Code, rec.Body)
			}
			if tc.status == 422 && !strings.Contains(rec.Body.String(), "venue rule refused") {
				t.Fatal("pre-check reason omitted")
			}
			if tc.code == "destination_ambiguous" && !strings.Contains(rec.Body.String(), `"candidates"`) {
				t.Fatal("ambiguity candidates omitted")
			}
		})
	}
}

func TestOpenAPITradingContract(t *testing.T) {
	spec := string(openapiSpec)
	paths := map[string]bool{}
	path := ""
	for _, line := range strings.Split(spec, "\n") {
		if line == "components:" {
			break
		}
		if strings.HasPrefix(line, "  /") && strings.HasSuffix(line, ":") {
			path = strings.TrimSuffix(strings.TrimSpace(line), ":")
		}
		for _, method := range []string{"get", "post", "put", "delete"} {
			if line == "    "+method+":" {
				paths[strings.ToUpper(method)+" "+path] = true
			}
		}
	}
	for _, route := range NewRouteRegistry(&fakeService{}, nil).Routes() {
		if strings.HasPrefix(route.Pattern, "/trading") || strings.HasSuffix(route.Pattern, "/trading-destinations") {
			if !paths[route.Method+" "+route.Pattern] {
				t.Fatalf("undocumented route: %s %s", route.Method, route.Pattern)
			}
		}
	}
	if !paths["POST /orders/send-paper"] {
		t.Fatal("send-paper is missing")
	}
	sendPaper := strings.SplitN(strings.SplitN(spec, "  /orders/send-paper:", 2)[1],
		"  /orders/drop-copy/submit:", 2)[0]
	if !strings.Contains(sendPaper, "        \"501\":\n          $ref: \"#/components/responses/NotImplementedError\"") {
		t.Fatal("send-paper must document 501 when trading runtime is absent")
	}
	restore := strings.SplitN(strings.SplitN(spec, "  /backup/restore:", 2)[1],
		"  /business-csv/export:", 2)[0]
	if !strings.Contains(restore, "        \"409\":\n          $ref: \"#/components/responses/TradingConflictError\"") ||
		!strings.Contains(spec, "\n    TradingConflictError:\n") {
		t.Fatal("backup restore must document 409 fee_account_required for a group grant without a fee account")
	}
	for _, snippet := range []string{
		"required: [providers, connections, credentialsSealed]",
		"required: [id, provider, label, mode, enabled, venueIdentity, feeAccount, state, secrets, instruments, access]",
		"required: [provider, label, mode, credentials, enabled]",
		"required: [connection, venueAccount, route, clientOrderId, sendAttempted, createdAt]",
		"required: [symbol, tradable]", "required: [supported, exists, tradable]",
		"TradingCredentials:\n      type: object", "route:\n          type: object",
		"enum: [acknowledged, refused, unknown]", "SendPaperOrderRequest:",
		"SendPaperOrderResponse:", "required: [route]", "/orders/send-live", "comes with the live-trading token.",
		"enum: [paper, live]", "enum: [running, unavailable, stopped]",
		"live_trading_unavailable", "trading_mode_mismatch", "fee_account_required",
		"venue_order_managed", "no_destination", "destination_ambiguous",
		"required: [candidates]", "tradingCredentialsUnavailable:",
		"tradingConnections:", "tradingInstruments:", "tradingAccess:", "venueOrders:",
		"- trading", "Trading travels with activity_history",
		"restored disabled with no keys", "existing venue-order links",
		"force. Force-deleting an account removes its venue-fee adjustments",
		"leavesQuantity 0", "after both success and failure",
		"409 fee_account_required; the archived connection must",
		"fee account from the archive).",
		"grants are deleted with it and never return `has_dependents`",
		"former members keep only their own grants.",
		"through a group grant; an own grant of the account is unaffected.",
	} {
		if !strings.Contains(spec, snippet) {
			t.Fatalf("OpenAPI contract missing %q", snippet)
		}
	}
	for _, schema := range []struct {
		name  string
		value any
	}{
		{"TradingAccess", tradingAccessDTO{}},
		{"BackupTradingAccess", backup.TradingAccess{}},
	} {
		wantRequired, wantProperties := jsonMembers(reflect.TypeOf(schema.value))
		gotRequired, gotProperties := openAPISchemaMembers(t, spec, schema.name)
		if !reflect.DeepEqual(gotRequired, wantRequired) || !reflect.DeepEqual(gotProperties, wantProperties) {
			t.Fatalf("OpenAPI %s = required %v properties %v, want required %v properties %v as its Go type encodes",
				schema.name, gotRequired, gotProperties, wantRequired, wantProperties)
		}
	}
	if _, properties := openAPISchemaMembers(t, spec, "TradingAccessRequest"); !reflect.DeepEqual(properties, []string{"account", "accountGroup", "venueAccount"}) {
		t.Fatalf("OpenAPI TradingAccessRequest properties = %v, want the PUT access body members", properties)
	}
	access := strings.SplitN(strings.SplitN(spec, "  /trading/connections/{id}/access:", 2)[1],
		"  /trading/connections/{id}/fee-account:", 2)[0]
	for _, parameter := range []string{"- name: account\n", "- name: accountGroup\n"} {
		if !strings.Contains(access, parameter) {
			t.Fatalf("DELETE access query parameter %q is undocumented", parameter)
		}
	}
}

// jsonMembers lists the JSON member names of a struct type, sorted: the ones
// encoded always, without omitempty, and all of them.
func jsonMembers(typ reflect.Type) (required, properties []string) {
	for i := 0; i < typ.NumField(); i++ {
		name, options, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		properties = append(properties, name)
		if options != "omitempty" {
			required = append(required, name)
		}
	}
	slices.Sort(required)
	slices.Sort(properties)
	return required, properties
}

// openAPISchemaMembers reads one components schema's required list and
// property names from the spec text, sorted.
func openAPISchemaMembers(t *testing.T, spec, name string) (required, properties []string) {
	t.Helper()
	_, schema, found := strings.Cut(spec, "\n    "+name+":\n")
	if !found {
		t.Fatalf("OpenAPI schema %s is missing", name)
	}
	inProperties := false
	for _, line := range strings.Split(schema, "\n") {
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if strings.TrimSpace(line) != "" && indent < 6 {
			break
		}
		switch {
		case indent == 6:
			inProperties = line == "      properties:"
			if list, ok := strings.CutPrefix(line, "      required: ["); ok {
				required = strings.Split(strings.TrimSuffix(list, "]"), ", ")
			}
		case indent == 8 && inProperties:
			properties = append(properties, strings.TrimSuffix(strings.TrimSpace(line), ":"))
		}
	}
	slices.Sort(required)
	slices.Sort(properties)
	return required, properties
}
