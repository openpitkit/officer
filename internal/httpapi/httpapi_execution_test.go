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
	"bytes"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
)

func TestCheckOrder_Pass(t *testing.T) {
	svc := &fakeService{checkResult: domain.CheckResult{
		Passed: true, WouldLockPrices: []string{"100"},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"account":"acc-1","baseAsset":"AAPL","quoteAsset":"USD","side":"buy","amountKind":"quantity","amountValue":"1","price":"100"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/orders/check", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	check, ok := m["check"].(map[string]any)
	if !ok {
		t.Fatalf("want check object, got %v", m["check"])
	}
	if check["passed"] != true {
		t.Fatalf("want passed=true, got %v", check["passed"])
	}
	prices, ok := check["wouldDisplayPrices"].([]any)
	if !ok || len(prices) != 1 || prices[0] != "100" {
		t.Fatalf("want wouldDisplayPrices=[100], got %v", check["wouldDisplayPrices"])
	}
	if check["wouldBlock"] != nil {
		t.Fatalf("want wouldBlock=null, got %v", check["wouldBlock"])
	}
}

func TestCheckOrder_Reject(t *testing.T) {
	svc := &fakeService{checkResult: domain.CheckResult{
		Passed: false,
		Rejects: []domain.OrderReject{
			{Code: "insufficient_funds", Scope: "account", Policy: "spot_funds", Reason: "no funds"},
		},
		WouldBlock: &domain.ExecutionAccountBlock{Account: "acc-1", Code: "account_blocked"},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"account":"acc-1","baseAsset":"AAPL","quoteAsset":"USD","side":"buy","amountKind":"quantity","amountValue":"1"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/orders/check", body))
	// An engine reject is a successful 200, not an HTTP error.
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	check, _ := m["check"].(map[string]any)
	if check["passed"] != false {
		t.Fatalf("want passed=false, got %v", check["passed"])
	}
	rejects, ok := check["rejects"].([]any)
	if !ok || len(rejects) != 1 {
		t.Fatalf("want 1 reject, got %v", check["rejects"])
	}
	rej, _ := rejects[0].(map[string]any)
	if rej["code"] != "insufficient_funds" || rej["scope"] != "account" {
		t.Fatalf("reject fields not on the wire: %v", rej)
	}
	block, ok := check["wouldBlock"].(map[string]any)
	if !ok || block["account"] != "acc-1" || block["code"] != "account_blocked" {
		t.Fatalf("wouldBlock not on the wire: %v", check["wouldBlock"])
	}
}

func TestCheckOrder_InvalidJSON(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/orders/check", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestCheckOrder_ValidationError(t *testing.T) {
	// The backend rejects a malformed account/asset with ErrInvalid; the handler
	// maps it to 400, mirroring submit.
	svc := &fakeService{stateErr: domain.ErrInvalid}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"account":"acc-1","baseAsset":"bad asset","quoteAsset":"USD","side":"buy","amountKind":"quantity","amountValue":"1"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/orders/check", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

// TestSubmitOrder_Created checks the submit path returns 201: the order row is
// persisted on every success path (even an engine reject), so the resource-
// creating POST is a 201 Created carrying the order.
func TestSubmitOrder_Created(t *testing.T) {
	svc := &fakeService{submitOrder: domain.Order{
		ExternalID: extID("order-1"), Account: "acc-1", BaseAsset: "AAPL",
		QuoteAsset: "USD", Side: domain.OrderSideBuy,
		Status: domain.OrderStatusCommitted,
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"account":"acc-1","baseAsset":"AAPL","quoteAsset":"USD","side":"buy","amountKind":"quantity","amountValue":"1","price":"100"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/orders", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	if _, ok := m["order"].(map[string]any); !ok {
		t.Fatalf("want order object, got %v", m["order"])
	}
	submit, ok := m["submitResponse"].(map[string]any)
	if !ok {
		t.Fatalf("want submitResponse object, got %v", m["submitResponse"])
	}
	if submit["token"] != "submit-token" ||
		submit["keyId"] != "key-1" ||
		submit["orderExternalId"] != extID("order-1").String() ||
		submit["verdict"] != "accept" {
		t.Fatalf("unexpected submitResponse: %v", submit)
	}
}

func TestSubmitOrder_RiskRejectReturnsSignedDecision(t *testing.T) {
	reject := map[string]any{
		"code":    "max_order_size",
		"scope":   "order",
		"policy":  "order-size",
		"reason":  "order too large",
		"details": "qty=100",
	}
	svc := &fakeService{
		submitOrder: domain.Order{
			ExternalID: extID("order-1"), Account: "acc-1", BaseAsset: "AAPL",
			QuoteAsset: "USD", Side: domain.OrderSideBuy,
			Status: domain.OrderStatusRejected,
		},
		submitOrderToken: backend.ApprovalToken{
			Token:           "reject-token",
			KeyID:           "key-1",
			OrderExternalID: extID("order-1").String(),
			Verdict:         "reject",
			Reasons: []domain.OrderReject{{
				Code:    "max_order_size",
				Scope:   "order",
				Policy:  "order-size",
				Reason:  "order too large",
				Details: "qty=100",
			}},
			Signed: true,
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"account":"acc-1","baseAsset":"AAPL","quoteAsset":"USD","side":"buy","amountKind":"quantity","amountValue":"100","price":"100"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/orders", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	m := bodyMap(t, rec.Result())
	submit, ok := m["submitResponse"].(map[string]any)
	if !ok {
		t.Fatalf("want submitResponse object, got %v", m["submitResponse"])
	}
	if submit["token"] != "reject-token" ||
		submit["keyId"] != "key-1" ||
		submit["orderExternalId"] != extID("order-1").String() ||
		submit["verdict"] != "reject" {
		t.Fatalf("unexpected submitResponse: %v", submit)
	}
	reasons, ok := submit["reasons"].([]any)
	if !ok || len(reasons) != 1 {
		t.Fatalf("want one reject reason, got %v", submit["reasons"])
	}
	if got := reasons[0]; !reflect.DeepEqual(got, reject) {
		t.Fatalf("reject reason = %#v, want %#v", got, reject)
	}
}

// TestSubmitOrder_ValidationError checks malformed order input (a bad enum,
// decimal, or asset the engine mapper rejects with domain.ErrInvalid) surfaces
// as 400, not the 500 default. The fake stands in for the engine mapper raising
// ErrInvalid for, e.g., an unknown amount kind or a non-decimal amount.
func TestSubmitOrder_ValidationError(t *testing.T) {
	svc := &fakeService{stateErr: domain.ErrInvalid}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"account":"acc-1","baseAsset":"AAPL","quoteAsset":"USD","side":"buy","amountKind":"base","amountValue":"1"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/orders", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

// TestApplyAdjustment_Created checks the adjustment path returns 201: the
// adjustment record is appended on every success path (accept or reject), so the
// resource-creating POST is a 201 Created carrying the record.
func TestApplyAdjustment_Created(t *testing.T) {
	svc := &fakeService{adjustment: domain.AccountAdjustmentRecord{
		ExternalID: extID("adj-1"), Account: "acc-1",
		Request: domain.AdjustmentRequest{Asset: "USD"},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"asset":"USD","balance":{"mode":"delta","value":"100"}}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/accounts/acc-1/adjustments", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	adj, ok := m["adjustment"].(map[string]any)
	if !ok {
		t.Fatalf("want adjustment object, got %v", m["adjustment"])
	}
	if adj["externalId"] != extID("adj-1").String() {
		t.Fatalf("want externalId=%s, got %v", extID("adj-1").String(), adj["externalId"])
	}
	assertNoSurrogateID(t, adj)
}

func TestApplyAdjustment_NoChangeReturnsNoContent(t *testing.T) {
	svc := &fakeService{stateErr: domain.ErrNoChange}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"asset":"USD","balance":{"mode":"delta","value":"0"}}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/accounts/acc-1/adjustments", body))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("response body = %q, want empty", rec.Body.String())
	}
}

// TestApplyExecutionReport_Created checks the execution-report path returns 201:
// the trade row is created on the success path, so the resource-creating POST is
// a 201 Created carrying the result.
func TestApplyExecutionReport_Created(t *testing.T) {
	svc := &fakeService{orderDetail: domain.OrderDetail{
		Order: domain.Order{
			ExternalID: extID("order-1"), Account: "acc-1", BaseAsset: "AAPL",
			QuoteAsset: "USD", Side: domain.OrderSideBuy,
		},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"quantity":"1","price":"100","leavesQuantity":"0","status":"cancelled","force":true}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/orders/"+extID("order-1").String()+"/execution-reports", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	if _, ok := m["result"].(map[string]any); !ok {
		t.Fatalf("want result object, got %v", m["result"])
	}
	if !svc.execReportIn.Force {
		t.Fatal("force was not forwarded to ApplyExecutionReport")
	}
	if svc.execReportIn.LeavesQuantity != "0" {
		t.Fatalf("leavesQuantity not forwarded: %q", svc.execReportIn.LeavesQuantity)
	}
	if svc.execReportIn.OrderStatus != domain.OrderStatusCancelled {
		t.Fatalf("status not forwarded: %q", svc.execReportIn.OrderStatus)
	}
	if svc.execReportIn.Account != "" ||
		svc.execReportIn.BaseAsset != "" ||
		svc.execReportIn.QuoteAsset != "" ||
		svc.execReportIn.Side != "" {
		t.Fatalf("order-derived fields were populated by HTTP: %+v", svc.execReportIn)
	}
}

// TestApplyExecutionReport_MissingLeavesQuantity checks the handler rejects a
// fill body that omits leavesQuantity with 400 validation, locking in the
// engine's hard requirement that every report carries leaves before it reaches
// the service.
func TestApplyExecutionReport_MissingLeavesQuantity(t *testing.T) {
	svc := &fakeService{orderDetail: domain.OrderDetail{
		Order: domain.Order{
			ExternalID: extID("order-1"), Account: "acc-1", BaseAsset: "AAPL",
			QuoteAsset: "USD", Side: domain.OrderSideBuy,
		},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"quantity":"1","price":"100","status":"filled"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/orders/"+extID("order-1").String()+"/execution-reports", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

// TestApplyExecutionReport_NonFillMissingLeaves checks a no-trade lifecycle
// report without leavesQuantity is rejected with 400: leaves is required on
// every report, not only fills.
func TestApplyExecutionReport_NonFillMissingLeaves(t *testing.T) {
	svc := &fakeService{orderDetail: domain.OrderDetail{
		Order: domain.Order{
			ExternalID: extID("order-1"), Account: "acc-1", BaseAsset: "AAPL",
			QuoteAsset: "USD", Side: domain.OrderSideBuy,
		},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"status":"cancelled"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/orders/"+extID("order-1").String()+"/execution-reports", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

// TestApplyExecutionReport_NonFillForwardsLeaves checks a no-trade lifecycle
// report that carries leavesQuantity is forwarded verbatim to the service.
func TestApplyExecutionReport_NonFillForwardsLeaves(t *testing.T) {
	svc := &fakeService{orderDetail: domain.OrderDetail{
		Order: domain.Order{
			ExternalID: extID("order-1"), Account: "acc-1", BaseAsset: "AAPL",
			QuoteAsset: "USD", Side: domain.OrderSideBuy,
		},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"status":"cancelled","leavesQuantity":"0"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/orders/"+extID("order-1").String()+"/execution-reports", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rec.Code)
	}
	if svc.execReportIn.OrderStatus != domain.OrderStatusCancelled {
		t.Fatalf("status not forwarded: %q", svc.execReportIn.OrderStatus)
	}
	if svc.execReportIn.LeavesQuantity != "0" {
		t.Fatalf("leavesQuantity not forwarded: %q", svc.execReportIn.LeavesQuantity)
	}
}

func TestApplyExecutionReport_InvalidStatus(t *testing.T) {
	svc := &fakeService{orderDetail: domain.OrderDetail{
		Order: domain.Order{
			ExternalID: extID("order-1"), Account: "acc-1", BaseAsset: "AAPL",
			QuoteAsset: "USD", Side: domain.OrderSideBuy,
		},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"quantity":"1","price":"100","leavesQuantity":"0","status":"done"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/orders/"+extID("order-1").String()+"/execution-reports", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
	if svc.execReportIn.Order != "" {
		t.Fatalf("invalid status reached service: %+v", svc.execReportIn)
	}
}

func TestApplyExecutionReport_MissingStatus(t *testing.T) {
	svc := &fakeService{orderDetail: domain.OrderDetail{
		Order: domain.Order{
			ExternalID: extID("order-1"), Account: "acc-1", BaseAsset: "AAPL",
			QuoteAsset: "USD", Side: domain.OrderSideBuy,
		},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"quantity":"1","price":"100","leavesQuantity":"0"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/orders/"+extID("order-1").String()+"/execution-reports", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}
