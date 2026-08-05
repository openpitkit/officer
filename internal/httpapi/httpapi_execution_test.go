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
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
)

func TestCheckOrder_Pass(t *testing.T) {
	svc := &fakeService{checkResult: domain.CheckResult{
		Passed: true, WouldLockPrice: "100",
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
	if check["wouldDisplayPrice"] != "100" {
		t.Fatalf("want wouldDisplayPrice=100, got %v", check["wouldDisplayPrice"])
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

func TestCheckOrder_PreservesEveryEngineRejectInOrder(t *testing.T) {
	want := []domain.OrderReject{
		{Code: "first", Scope: "order", Policy: "size", Reason: "r1", Details: "d1"},
		{Code: "second", Scope: "account", Policy: "funds", Reason: "r2", Details: "d2"},
		{Code: "third", Scope: "group", Policy: "block", Reason: "r3", Details: "d3"},
	}
	svc := &fakeService{checkResult: domain.CheckResult{Rejects: want}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"account":"acc-1","baseAsset":"AAPL","quoteAsset":"USD",` +
			`"side":"buy","amountKind":"quantity","amountValue":"1"}`,
	)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/orders/check", body,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	response := bodyMap(t, rec.Result())
	check, _ := response["check"].(map[string]any)
	got, _ := check["rejects"].([]any)
	if len(got) != len(want) {
		t.Fatalf("rejects = %+v", got)
	}
	for index, reject := range want {
		item, _ := got[index].(map[string]any)
		if item["code"] != reject.Code || item["scope"] != reject.Scope ||
			item["policy"] != reject.Policy || item["reason"] != reject.Reason ||
			item["details"] != reject.Details {
			t.Fatalf("reject[%d] = %+v, want %+v", index, item, reject)
		}
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
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

func TestSubmitOrderToken_Created(t *testing.T) {
	svc := &fakeService{approvalToken: backend.ApprovalToken{
		Token:   "submit-token",
		KeyID:   "key-1",
		Verdict: "accept",
		Signed:  true,
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"account":"acc-1","baseAsset":"AAPL","quoteAsset":"USD","side":"buy","amountKind":"quantity","amountValue":"1","price":"100"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/orders/submit?missingAccount=create", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	if m["token"] != "submit-token" ||
		m["keyId"] != "key-1" ||
		m["id"] != extID("generated-order").String() ||
		m["verdict"] != "accept" {
		t.Fatalf("unexpected submit response: %v", m)
	}
}

func TestSubmitDropCopyOrder_UsesDistinctUnsignedOperation(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		source domain.Source
	}{
		{name: "api", prefix: "/api/v1", source: domain.SourceAPI},
		{name: "panel", prefix: "/app/api/v1", source: domain.SourcePanel},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeService{}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			body := bytes.NewBufferString(
				`{"id":"drop-copy-1","account":"acc-1","baseAsset":"AAPL","quoteAsset":"USD",` +
					`"side":"buy","amountKind":"quantity","amountValue":"1",` +
					`"price":"100"}`,
			)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(
				http.MethodPost, tt.prefix+"/orders/drop-copy/submit?missingAccount=create", body,
			))
			if rec.Code != http.StatusCreated {
				t.Fatalf("want 201, got %d body=%s", rec.Code, rec.Body.String())
			}
			if !svc.submitOrderIn.DropCopy {
				t.Fatal("drop-copy operation did not reach the distinct service method")
			}
			if svc.submitOrderIn.Source != tt.source ||
				svc.submitOrderIn.Principal != domain.PrincipalOperator {
				t.Fatalf(
					"drop-copy caller = %+v, want %s operator",
					svc.submitOrderIn, tt.source,
				)
			}
			m := bodyMap(t, rec.Result())
			if m["id"] != "drop-copy-1" || m["status"] != "committed" {
				t.Fatalf("unexpected response: %v", m)
			}
			if _, present := m["token"]; present {
				t.Fatalf("drop-copy response contains token: %v", m)
			}
		})
	}
}

func TestSubmitDropCopyOrder_GeneratesIDWhenOmitted(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"account":"acc-1","baseAsset":"AAPL","quoteAsset":"USD",` +
			`"side":"buy","amountKind":"quantity","amountValue":"1"}`,
	)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/orders/drop-copy/submit?missingAccount=create", body,
	))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !svc.submitOrderIn.ExternalID.IsZero() {
		t.Fatalf("omitted id reached service as %q, want zero", svc.submitOrderIn.ExternalID)
	}
	response := bodyMap(t, rec.Result())
	if response["id"] != extID("generated-drop-copy-order").String() {
		t.Fatalf("response id = %v, want generated id", response["id"])
	}
	if response["status"] != string(domain.OrderStatusCommitted) {
		t.Fatalf("response status = %v, want committed", response["status"])
	}
}

func TestSubmitDropCopyOrder_ValidationErrorPreservesEngineMessage(t *testing.T) {
	svc := &fakeService{
		signingErr: fmt.Errorf(
			"engine: execute pre-trade: failed to access field 'limit price': %w",
			domain.ErrInvalid,
		),
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"account":"acc-1","baseAsset":"AAPL","quoteAsset":"USD",` +
			`"side":"buy","amountKind":"quantity","amountValue":"1"}`,
	)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/orders/drop-copy/submit?missingAccount=create", body,
	))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
	if errObj["message"] !=
		"engine: execute pre-trade: failed to access field 'limit price': invalid" {
		t.Fatalf("unexpected validation message: %v", errObj["message"])
	}
}

func TestSubmitOrderToken_ForwardsCallerSuppliedID(t *testing.T) {
	supplied := extID("client-order-1")
	svc := &fakeService{approvalToken: backend.ApprovalToken{
		Token: "submit-token", Verdict: "accept",
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"id":"` + supplied.String() + `","account":"acc-1","baseAsset":"AAPL",` +
			`"quoteAsset":"USD","side":"buy","amountKind":"quantity","amountValue":"1"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/orders/submit?missingAccount=create", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.submitOrderIn.ExternalID != supplied {
		t.Fatalf("submitted id = %q, want %q", svc.submitOrderIn.ExternalID, supplied)
	}
	m := bodyMap(t, rec.Result())
	if m["id"] != supplied.String() {
		t.Fatalf("response = %v, want id=%s", m, supplied)
	}
	if _, leaked := m["externalId"]; leaked {
		t.Fatalf("response leaked externalId: %v", m)
	}
}

func TestSubmitOrderToken_RiskRejectResponse(t *testing.T) {
	reject := map[string]any{
		"code":    "max_order_size",
		"scope":   "order",
		"policy":  "order-size",
		"reason":  "order too large",
		"details": "qty=100",
	}
	svc := &fakeService{
		approvalToken: backend.ApprovalToken{
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
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/orders/submit?missingAccount=create", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	m := bodyMap(t, rec.Result())
	if m["token"] != "reject-token" ||
		m["keyId"] != "key-1" ||
		m["id"] != extID("generated-order").String() ||
		m["verdict"] != "reject" {
		t.Fatalf("unexpected submit response: %v", m)
	}
	reasons, ok := m["reasons"].([]any)
	if !ok || len(reasons) != 1 {
		t.Fatalf("want one reject reason, got %v", m["reasons"])
	}
	if got := reasons[0]; !reflect.DeepEqual(got, reject) {
		t.Fatalf("reject reason = %#v, want %#v", got, reject)
	}
}

// TestSubmitOrder_ValidationError checks malformed order input (a bad enum,
// decimal, or asset the engine mapper rejects with domain.ErrInvalid) surfaces
// as 400, not the 500 default. The fake stands in for the engine mapper raising
// ErrInvalid for, e.g., an unknown amount kind or a non-decimal amount.
func TestSubmitOrderToken_ValidationError(t *testing.T) {
	svc := &fakeService{signingErr: domain.ErrInvalid}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"account":"acc-1","baseAsset":"AAPL","quoteAsset":"USD","side":"buy","amountKind":"base","amountValue":"1"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/orders/submit?missingAccount=create", body))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

func TestSubmitOrderLegacyEndpointGone(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(
		rec,
		httptest.NewRequest(http.MethodPost, "/api/v1/orders", bytes.NewBufferString(`{}`)),
	)
	if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
		t.Fatalf("POST /orders status = %d, want 404 or 405", rec.Code)
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
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/accounts/acc-1/adjustments?missingAccount=create", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	adj, ok := m["adjustment"].(map[string]any)
	if !ok {
		t.Fatalf("want adjustment object, got %v", m["adjustment"])
	}
	if adj["id"] != extID("adj-1").String() {
		t.Fatalf("want id=%s, got %v", extID("adj-1").String(), adj["id"])
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
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/accounts/acc-1/adjustments?missingAccount=create", body))
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
	svc := &fakeService{
		orderDetail: domain.OrderDetail{Order: domain.Order{
			ExternalID: extID("order-1"), Account: "acc-1", BaseAsset: "AAPL",
			QuoteAsset: "USD", Side: domain.OrderSideBuy,
		}},
		execReportResult: engine.ExecutionReportResult{ReportID: extID("report-1")},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"id":"report-1","quantity":"1","price":"100","leavesQuantity":"0","status":"cancelled","force":true,` +
			`"commission":{"amount":"-0.12","currency":"USD"}}`)
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
	if svc.execReportIn.Commission == nil ||
		svc.execReportIn.Commission.Amount != "-0.12" ||
		svc.execReportIn.Commission.Currency != "USD" {
		t.Fatalf("commission not forwarded: %+v", svc.execReportIn.Commission)
	}
	if svc.execReportIn.OrderStatus != domain.OrderStatusCancelled {
		t.Fatalf("status not forwarded: %q", svc.execReportIn.OrderStatus)
	}
	if svc.execReportIn.ExternalID != "report-1" {
		t.Fatalf("id not forwarded: %q", svc.execReportIn.ExternalID)
	}
	if m["id"] != svc.execReportResult.ReportID.String() {
		t.Fatalf("response id = %v, want %s", m["id"], svc.execReportResult.ReportID)
	}
	if svc.execReportIn.Account != "" ||
		svc.execReportIn.BaseAsset != "" ||
		svc.execReportIn.QuoteAsset != "" ||
		svc.execReportIn.Side != "" {
		t.Fatalf("order-derived fields were populated by HTTP: %+v", svc.execReportIn)
	}
}

func TestApplyExecutionReport_CommissionOnlyForwardsCallerFieldsAndEngineResult(
	t *testing.T,
) {
	svc := &fakeService{execReportResult: engine.ExecutionReportResult{
		ReportID: extID("commission-report"),
		Blocks: []domain.ExecutionAccountBlock{
			{Account: "acc-1", Code: "first"},
			{Account: "acc-1", Code: "second"},
		},
		Outcomes: []engine.BalanceOutcome{
			{Asset: "EUR", Outcome: domain.AdjustmentOutcomeAccepted{
				BalanceDelta: "1", BalanceResult: "2",
			}},
			{Asset: "USD", Outcome: domain.AdjustmentOutcomeAccepted{
				BalanceDelta: "-3", BalanceResult: "4",
			}},
		},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"status":"accepted","leavesQuantity":"7.5",` +
			`"commission":{"amount":"-0.12","currency":"EUR"}}`,
	)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost,
		"/api/v1/orders/"+extID("order-1").String()+"/execution-reports",
		body,
	))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	in := svc.execReportIn
	if in.FillQuantity != "" || in.FillPrice != "" || in.LockPrice != "" ||
		len(in.Lock) != 0 {
		t.Fatalf("Officer invented trade fields: %+v", in)
	}
	if in.LeavesQuantity != "7.5" || in.OrderStatus != domain.OrderStatusAccepted ||
		in.Commission == nil || in.Commission.Amount != "-0.12" ||
		in.Commission.Currency != "EUR" {
		t.Fatalf("caller fields were not forwarded exactly: %+v", in)
	}

	response := bodyMap(t, rec.Result())
	if response["id"] != extID("commission-report").String() {
		t.Fatalf("id = %v", response["id"])
	}
	result, _ := response["result"].(map[string]any)
	blocks, _ := result["blocks"].([]any)
	outcomes, _ := result["outcomes"].([]any)
	if len(blocks) != 2 || len(outcomes) != 2 {
		t.Fatalf("result = %+v", result)
	}
	firstBlock, _ := blocks[0].(map[string]any)
	secondBlock, _ := blocks[1].(map[string]any)
	firstOutcome, _ := outcomes[0].(map[string]any)
	secondOutcome, _ := outcomes[1].(map[string]any)
	if firstBlock["code"] != "first" || secondBlock["code"] != "second" ||
		firstOutcome["asset"] != "EUR" || secondOutcome["asset"] != "USD" {
		t.Fatalf("Engine result order changed: %+v", result)
	}
}

func TestApplyExecutionReport_GeneratesIDWhenOmitted(t *testing.T) {
	svc := &fakeService{
		execReportResult: engine.ExecutionReportResult{ReportID: extID("generated-report")},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"leavesQuantity":"0","status":"cancelled","force":true}`,
	)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost,
		"/api/v1/orders/"+extID("order-1").String()+"/execution-reports",
		body,
	))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !svc.execReportIn.ExternalID.IsZero() {
		t.Fatalf("request id = %q, want unset", svc.execReportIn.ExternalID)
	}
	m := bodyMap(t, rec.Result())
	if m["id"] != svc.execReportResult.ReportID.String() {
		t.Fatalf("response id = %v, want %s", m["id"], svc.execReportResult.ReportID)
	}
}

func TestApplyExecutionReport_DuplicateIDReturnsConflict(t *testing.T) {
	svc := &fakeService{execReportErr: domain.ErrAlreadyExists}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"id":"duplicate-report","leavesQuantity":"0","status":"cancelled","force":true}`,
	)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost,
		"/api/v1/orders/"+extID("order-1").String()+"/execution-reports",
		body,
	))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestApplyExecutionReport_MissingLeavesQuantity checks the handler rejects a
// fill body that omits leavesQuantity with 400 validation, locking in the
// engine's hard requirement that every settled report carries leaves before it
// reaches the service.
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
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

// TestApplyExecutionReport_NonFillOmitsLeaves checks a workflow-only report
// reaches the service without leavesQuantity.
func TestApplyExecutionReport_NonFillOmitsLeaves(t *testing.T) {
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
	body := bytes.NewBufferString(`{"status":"accepted"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/orders/"+extID("order-1").String()+"/execution-reports", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rec.Code)
	}
	if svc.execReportIn.OrderStatus != domain.OrderStatusAccepted {
		t.Fatalf("status not forwarded: %q", svc.execReportIn.OrderStatus)
	}
	if svc.execReportIn.LeavesQuantity != "" {
		t.Fatalf("leavesQuantity = %q, want empty", svc.execReportIn.LeavesQuantity)
	}
}

// TestApplyExecutionReport_NonFillForwardsOptionalLeaves locks in the API
// contract that leavesQuantity is optional for a workflow-only report, but is
// still forwarded and persisted by lower layers when the caller supplies it.
func TestApplyExecutionReport_NonFillForwardsOptionalLeaves(t *testing.T) {
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
		`{"status":"committed","leavesQuantity":"1.5"}`,
	)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost,
		"/api/v1/orders/"+extID("order-1").String()+"/execution-reports",
		body,
	))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if svc.execReportIn.OrderStatus != domain.OrderStatusCommitted ||
		svc.execReportIn.LeavesQuantity != "1.5" {
		t.Fatalf("optional workflow leaves not forwarded: %+v", svc.execReportIn)
	}
}

func TestApplyExecutionReport_WorkflowRejectsInvalidSettlementFields(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{
			name: "lock price",
			body: `{"status":"submitted","lockPrice":"100"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(
				http.MethodPost,
				"/api/v1/orders/"+extID("order-1").String()+"/execution-reports",
				bytes.NewBufferString(tc.body),
			))
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("want 400, got %d; body=%s", rec.Code, rec.Body.String())
			}
			if !svc.execReportIn.Order.IsZero() {
				t.Fatalf("invalid workflow report reached service: %+v", svc.execReportIn)
			}
		})
	}
}

// Workflow-only reports bypass the SDK, so the HTTP boundary rejects malformed
// leaves before the value can reach storage.
func TestApplyExecutionReport_RejectsInvalidWorkflowLeaves(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{
			name: "malformed",
			body: `{"status":"accepted","leavesQuantity":"not-a-number"}`,
		},
		{
			name: "negative",
			body: `{"status":"committed","leavesQuantity":"-1"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(
				http.MethodPost,
				"/api/v1/orders/"+extID("order-1").String()+"/execution-reports",
				bytes.NewBufferString(tc.body),
			))
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("want 400, got %d: %s", rec.Code, rec.Body.String())
			}
			if !svc.execReportIn.Order.IsZero() {
				t.Fatalf("invalid workflow report reached service: %+v", svc.execReportIn)
			}
		})
	}
}

// The engine's post-trade path has no reject channel, so an engine-settled
// report without leaves must be refused at the boundary instead of blocking the
// account downstream.
func TestApplyExecutionReport_EngineSettledMissingLeavesRejected(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "terminal", body: `{"status":"cancelled"}`},
		{
			name: "commission",
			body: `{"status":"accepted","commission":{"amount":"-1","currency":"USD"}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(
				http.MethodPost,
				"/api/v1/orders/"+extID("order-1").String()+"/execution-reports",
				bytes.NewBufferString(tc.body),
			))
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("want 400, got %d: %s", rec.Code, rec.Body.String())
			}
			if !svc.execReportIn.Order.IsZero() {
				t.Fatalf("report without leaves reached service: %+v", svc.execReportIn)
			}
		})
	}
}

// TestApplyExecutionReport_TerminalForwardsReleaseQuantity checks a terminal
// no-trade report carries the remaining quantity through to engine settlement
// so the engine can release it before Officer records zero persisted leaves.
func TestApplyExecutionReport_TerminalForwardsReleaseQuantity(t *testing.T) {
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
	body := bytes.NewBufferString(`{"status":"cancelled","leavesQuantity":"2"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/orders/"+extID("order-1").String()+"/execution-reports", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rec.Code)
	}
	if svc.execReportIn.OrderStatus != domain.OrderStatusCancelled {
		t.Fatalf("status not forwarded: %q", svc.execReportIn.OrderStatus)
	}
	if svc.execReportIn.LeavesQuantity != "2" {
		t.Fatalf("leavesQuantity not forwarded: %q", svc.execReportIn.LeavesQuantity)
	}
}

// TestApplyExecutionReport_RejectsFillWithNonTerminalWorkflowStatus checks a
// fill (quantity+price) paired with a non-terminal workflow status
// (submitted/accepted/committed) is rejected with 400 validation before it
// reaches the service: such a report would settle through the engine while
// persisting an inconsistent lifecycle status.
func TestApplyExecutionReport_RejectsFillWithNonTerminalWorkflowStatus(t *testing.T) {
	for _, status := range []string{"submitted", "accepted", "committed"} {
		t.Run(status, func(t *testing.T) {
			svc := &fakeService{}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			body := bytes.NewBufferString(
				`{"quantity":"1","price":"100","leavesQuantity":"0","status":"` + status + `"}`)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
				"/api/v1/orders/"+extID("order-1").String()+"/execution-reports", body))
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("want 400, got %d", rec.Code)
			}
			m := bodyMap(t, rec.Result())
			errObj, _ := m["error"].(map[string]any)
			if errObj["code"] != "validation" {
				t.Fatalf("want code=validation, got %v", errObj["code"])
			}
			if svc.execReportIn.Order != "" {
				t.Fatalf("fill with non-terminal workflow status reached service: %+v",
					svc.execReportIn)
			}
		})
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
	if rec.Code != http.StatusUnprocessableEntity {
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
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}
