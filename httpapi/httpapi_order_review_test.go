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
	"reflect"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	fwsigning "go.openpit.dev/officer/framework/signing"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

func TestOrderAlertFailureREST(t *testing.T) {
	for _, operation := range []string{"submit", "send-paper", "send-live", "confirm", "force", "cancel"} {
		for _, step := range []string{"", "publish", "close"} {
			t.Run(operation+"/"+step, func(t *testing.T) {
				var failure *backend.AlertFailure
				if step != "" {
					failure = &backend.AlertFailure{Step: step, Message: "alert service unavailable"}
				}
				approval := backend.SignedApproval{
					Verdict: domain.VerdictReview, AlertFailure: failure,
					OrderExternalID: "review-order", Reviews: restReviewMarks(),
				}
				svc := &fakeService{
					signedApproval:     approval,
					venueSendResult:    backend.VenueSendResult{Approval: approval},
					submitOrder:        domain.Order{ExternalID: "review-order", Status: domain.OrderStatusCommitted},
					reviewAlertFailure: failure,
				}
				path := "/orders/" + operation
				status := http.StatusCreated
				request := `{"account":"desk","baseAsset":"AAPL","quoteAsset":"USD","side":"buy","amountKind":"quantity","amountValue":"10"`
				switch operation {
				case "submit":
					path += "?missingAccount=create"
					request += `,"mode":"hold"}`
				case "send-paper", "send-live":
					request += `,"destination":{"connection":"paper","route":{}}}`
				default:
					path = "/orders/review-order/review/" + operation
					status = http.StatusOK
					request = ""
					svc.venueSendResult.Approval.Verdict = domain.VerdictAccept
				}
				rec := callTrading(t, svc, "/api/v1", http.MethodPost, path, request)
				if rec.Code != status {
					t.Fatalf("status=%d; want %d: %s", rec.Code, status, rec.Body.String())
				}
				body := bodyMap(t, rec.Result())
				got, present := body["alertFailure"]
				if present != (failure != nil) {
					t.Fatalf("alertFailure=%v present=%t", got, present)
				}
				if present && !reflect.DeepEqual(got, map[string]any{"step": step, "message": failure.Message}) {
					t.Fatalf("alertFailure=%v", got)
				}
			})
		}
	}
}

func restReviewMarks() []domain.ReviewMark {
	return []domain.ReviewMark{{
		OrderReject: domain.OrderReject{Code: "size", Scope: "order", Policy: "size", Reason: "check size", Details: "limit exceeded"},
		Mode:        domain.RuleMode("review_without_hold"),
	}}
}

func assertRESTReviewMarks(t *testing.T, raw any) {
	t.Helper()
	want := []any{map[string]any{
		"code": "size", "scope": "order", "policy": "size", "reason": "check size",
		"details": "limit exceeded", "mode": "review_without_hold",
	}}
	if !reflect.DeepEqual(raw, want) {
		t.Fatalf("marks=%v; want %v", raw, want)
	}
}

func TestOrderReviewConfirmREST(t *testing.T) {
	for _, mount := range []string{"/api/v1", "/app/api/v1"} {
		for _, tc := range []struct {
			name    string
			verdict domain.Verdict
			trading bool
		}{
			{"gateway-accept", domain.VerdictAccept, false},
			{"gateway-reject", domain.VerdictReject, false},
			{"trading-accept", domain.VerdictAccept, true},
			{"trading-reject", domain.VerdictReject, true},
		} {
			t.Run(mount+"/"+tc.name, func(t *testing.T) {
				status := domain.OrderStatusCommitted
				var reasons []domain.OrderReject
				if tc.verdict == domain.VerdictReject {
					status = domain.OrderStatusAwaitingReview
					reasons = []domain.OrderReject{{Code: "funds", Reason: "insufficient"}}
				}
				svc := &fakeService{
					submitOrder: domain.Order{ExternalID: "review-order", Status: status},
					orderDetail: domain.OrderDetail{DisplayPrice: "123.45", Events: []domain.OrderEvent{{
						Attestation: &domain.EventAttestation{Alg: fwsigning.AlgEd25519},
					}}},
					venueSendResult: backend.VenueSendResult{Approval: backend.SignedApproval{
						Verdict: tc.verdict, Reasons: reasons, SignedApproval: "decision-envelope", KeyID: "key", Signed: true,
					}},
				}
				if tc.trading && tc.verdict == domain.VerdictAccept {
					svc.venueSendResult.Send = &backend.VenueSend{Status: "acknowledged", Order: &domain.VenueOrder{
						Connection: "paper", VenueAccount: "desk", Route: `{}`, ClientOrderID: "client", VenueOrderID: "remote",
					}}
				}
				rec := callTrading(t, svc, mount, http.MethodPost, "/orders/review-order/review/confirm", "")
				if rec.Code != http.StatusOK {
					t.Fatalf("confirm status=%d: %s", rec.Code, rec.Body.String())
				}
				body := bodyMap(t, rec.Result())
				order := body["order"].(map[string]any)
				if order["id"] != "review-order" || order["status"] != string(status) || order["displayPrice"] != "123.45" || order["signed"] != true {
					t.Fatalf("order=%v", order)
				}
				decision := body["decision"].(map[string]any)
				if len(decision) != 5 || decision["verdict"] != string(tc.verdict) || decision["signedApproval"] != "decision-envelope" || decision["keyId"] != "key" || decision["signed"] != true {
					t.Fatalf("decision=%v", decision)
				}
				if tc.verdict == domain.VerdictReject {
					rejects := decision["reasons"].([]any)
					if len(rejects) != 1 || rejects[0].(map[string]any)["code"] != "funds" {
						t.Fatalf("reasons=%v", rejects)
					}
				}
				send, present := body["venueSend"]
				if present != (tc.trading && tc.verdict == domain.VerdictAccept) {
					t.Fatalf("venueSend=%v present=%t", send, present)
				}
				if present {
					v := send.(map[string]any)
					if v["status"] != "acknowledged" || v["venueOrder"].(map[string]any)["connection"] != "paper" {
						t.Fatalf("venueSend=%v", v)
					}
				}
				if svc.reviewOrderID != "review-order" || svc.reviewConfirmCalls != 1 || svc.reviewCancelCalls != 0 {
					t.Fatal("review confirmation was not delegated exactly once")
				}
			})
		}
	}
}

func TestOrderReviewForceREST(t *testing.T) {
	for _, mount := range []string{"/api/v1", "/app/api/v1"} {
		for _, trading := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/trading=%t", mount, trading), func(t *testing.T) {
				svc := &fakeService{
					submitOrder: domain.Order{ExternalID: "review-order", Status: domain.OrderStatusCommitted},
					orderDetail: domain.OrderDetail{DisplayPrice: "123.45", Events: []domain.OrderEvent{{
						Attestation: &domain.EventAttestation{Alg: fwsigning.AlgEd25519},
					}}},
					venueSendResult: backend.VenueSendResult{Approval: backend.SignedApproval{
						Verdict: domain.VerdictAccept, SignedApproval: "force-envelope", KeyID: "key", Signed: true,
					}},
				}
				if trading {
					svc.venueSendResult.Send = &backend.VenueSend{Status: "acknowledged", Order: &domain.VenueOrder{
						Connection: "paper", VenueAccount: "desk", Route: `{}`, ClientOrderID: "client", VenueOrderID: "remote",
					}}
				}
				rec := callTrading(t, svc, mount, http.MethodPost, "/orders/review-order/review/force", "")
				if rec.Code != http.StatusOK {
					t.Fatalf("force status=%d: %s", rec.Code, rec.Body.String())
				}
				body := bodyMap(t, rec.Result())
				order := body["order"].(map[string]any)
				if order["id"] != "review-order" || order["status"] != "committed" || order["displayPrice"] != "123.45" || order["signed"] != true {
					t.Fatalf("order=%v", order)
				}
				decision := body["decision"].(map[string]any)
				if len(decision) != 5 || decision["verdict"] != "accept" || decision["signedApproval"] != "force-envelope" || decision["keyId"] != "key" || decision["signed"] != true || len(decision["reasons"].([]any)) != 0 {
					t.Fatalf("decision=%v", decision)
				}
				send, present := body["venueSend"]
				if present != trading {
					t.Fatalf("venueSend=%v present=%t", send, present)
				}
				if present {
					v := send.(map[string]any)
					if v["status"] != "acknowledged" || v["venueOrder"].(map[string]any)["connection"] != "paper" {
						t.Fatalf("venueSend=%v", v)
					}
				}
				if svc.reviewOrderID != "review-order" || svc.reviewForceCalls != 1 || svc.reviewConfirmCalls != 0 || svc.reviewCancelCalls != 0 {
					t.Fatalf("force calls=%d confirm=%d cancel=%d", svc.reviewForceCalls, svc.reviewConfirmCalls, svc.reviewCancelCalls)
				}
			})
		}
	}
}

func TestOrderReviewCancelREST(t *testing.T) {
	for _, mount := range []string{"/api/v1", "/app/api/v1"} {
		t.Run(mount, func(t *testing.T) {
			svc := &fakeService{
				submitOrder: domain.Order{ExternalID: "review-order", Status: domain.OrderStatusCancelled},
				attestation: backend.Attestation{SignedAttestation: "cancel-envelope", KeyID: "key", Signed: true},
			}
			rec := callTrading(t, svc, mount, http.MethodPost, "/orders/review-order/review/cancel", "")
			if rec.Code != http.StatusOK {
				t.Fatalf("cancel status=%d: %s", rec.Code, rec.Body.String())
			}
			body := bodyMap(t, rec.Result())
			if len(body) != 4 || body["signedAttestation"] != "cancel-envelope" || body["attestationKeyId"] != "key" || body["signed"] != true || body["order"].(map[string]any)["status"] != "cancelled" {
				t.Fatalf("cancel response=%v", body)
			}
			if svc.reviewOrderID != "review-order" || svc.reviewCancelCalls != 1 || svc.reviewConfirmCalls != 0 {
				t.Fatal("review cancellation was not delegated exactly once")
			}
		})
	}
}

func TestOrderReviewRESTErrors(t *testing.T) {
	for _, operation := range []string{"confirm", "cancel", "force"} {
		for _, tc := range []struct {
			name   string
			err    error
			status int
			code   string
		}{
			{"not-waiting", domain.ErrConflict, 409, "conflict"},
			{"approval", domain.ErrApprovalRequired, 409, "approval_required"},
			{"unknown", domain.ErrNotFound, 404, "not_found"},
			{"mcp", domain.ErrForbidden, 403, "forbidden"},
			{"venue-precheck", domain.ErrInvalid, 422, "validation"},
			{"runtime", domain.ErrNotImplemented, 501, "not_implemented"},
			{"upstream", domain.ErrUpstream, 502, "upstream"},
			{"restarting", domain.ErrEngineRestarting, 503, "engine_restarting"},
		} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				svc := &fakeService{reviewErr: fmt.Errorf("review decision: %w", tc.err)}
				rec := callTrading(t, svc, "/api/v1", http.MethodPost, "/orders/review-order/review/"+operation, "")
				if rec.Code != tc.status {
					t.Fatalf("status=%d; want %d: %s", rec.Code, tc.status, rec.Body.String())
				}
				if bodyMap(t, rec.Result())["error"].(map[string]any)["code"] != tc.code {
					t.Fatalf("error response=%s", rec.Body.String())
				}
			})
		}
		t.Run(operation+"/bad-id", func(t *testing.T) {
			svc := &fakeService{}
			router, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/orders/bad/review/"+operation, nil)
			req.URL.RawPath = "/api/v1/orders/%ZZ/review/" + operation
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != 400 || svc.reviewConfirmCalls != 0 || svc.reviewCancelCalls != 0 || svc.reviewForceCalls != 0 {
				t.Fatalf("bad id status=%d calls=%d/%d: %s", rec.Code, svc.reviewConfirmCalls, svc.reviewCancelCalls, rec.Body.String())
			}
		})
	}
}

func TestOrderReviewRESTSubmitSendAndCheck(t *testing.T) {
	for _, operation := range []string{"submit", "send-paper", "send-live", "check"} {
		t.Run(operation, func(t *testing.T) {
			approval := backend.SignedApproval{Verdict: domain.VerdictReview, Reviews: restReviewMarks(), SignedApproval: "review-envelope", KeyID: "key"}
			svc := &fakeService{
				signedApproval: approval, venueSendResult: backend.VenueSendResult{Approval: approval},
				checkResult: domain.CheckResult{Reviews: restReviewMarks()},
			}
			request := `{"account":"desk","baseAsset":"AAPL","quoteAsset":"USD","side":"buy","amountKind":"quantity","amountValue":"10"`
			status := 201
			switch operation {
			case "submit":
				request += `,"mode":"hold"`
			case "send-paper", "send-live":
				request += `,"destination":{"connection":"paper","route":{}}`
			case "check":
				status = 200
			}
			path := "/orders/" + operation
			if operation == "submit" {
				path += "?missingAccount=create"
			}
			rec := callTrading(t, svc, "/api/v1", http.MethodPost, path, request+`}`)
			if rec.Code != status {
				t.Fatalf("status=%d: %s", rec.Code, rec.Body.String())
			}
			body := bodyMap(t, rec.Result())
			if operation == "check" {
				body = body["check"].(map[string]any)
			} else if body["verdict"] != "review" || body["signedApproval"] != "review-envelope" {
				t.Fatalf("review response=%v", body)
			}
			assertRESTReviewMarks(t, body["reviews"])
			if _, exists := body["venueSend"]; exists {
				t.Fatal("review response must omit venueSend")
			}
		})
	}
}

func restReviewDetail(t *testing.T) domain.OrderDetail {
	t.Helper()
	token, err := fwsigning.BuildEnvelope(fwsigning.Envelope{Approval: domain.ApprovalPayload{Reviews: restReviewMarks(), Verdict: domain.VerdictReview}})
	if err != nil {
		t.Fatal(err)
	}
	return domain.OrderDetail{
		Order:  domain.Order{ExternalID: "review-order", Status: domain.OrderStatusAwaitingReview},
		Review: &domain.OrderReview{Mode: domain.SubmitModeHold},
		Events: []domain.OrderEvent{{Type: domain.OrderEventPreTradeReview, At: time.Unix(100, 0).UTC(),
			Attestation: &domain.EventAttestation{SignedEnvelope: token}}},
	}
}

func TestOrderReviewRESTGet(t *testing.T) {
	for _, tc := range []struct {
		trading bool
		held    bool
	}{
		{trading: false, held: false},
		{trading: false, held: true},
		{trading: true, held: false},
		{trading: true, held: true},
	} {
		trading, held := tc.trading, tc.held
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprintf("trading=%t/held=%t/failed=%t", trading, held, failed), func(t *testing.T) {
				detail := restReviewDetail(t)
				detail.Review.Held = held
				if trading {
					detail.Review.Destination = &domain.TradingDestination{Connection: "paper", VenueAccount: "desk", Route: `{"timeInForce":"day"}`}
				}
				if failed {
					detail.Events = append(detail.Events, domain.OrderEvent{
						Type: domain.OrderEventPreTradeRejected, At: time.Unix(101, 0).UTC(),
						Attestation: &domain.EventAttestation{RequestType: domain.AttestationRequestReviewConfirm},
						Payload:     domain.OrderEventPayload{Rejects: []domain.OrderReject{{Code: "funds", Reason: "insufficient"}}},
					})
				}
				svc := &fakeService{orderDetail: detail}
				rec := callTrading(t, svc, "/api/v1", http.MethodGet, "/orders/review-order", "")
				if rec.Code != 200 {
					t.Fatalf("get status=%d: %s", rec.Code, rec.Body.String())
				}
				view := bodyMap(t, rec.Result())["review"].(map[string]any)
				if len(view) != 4 || view["held"] != held {
					t.Fatalf("review=%v", view)
				}
				assertRESTReviewMarks(t, view["marks"])
				if failed {
					last := view["lastConfirm"].(map[string]any)
					if last["verdict"] != "reject" || last["at"] != "1970-01-01T00:01:41Z" || last["reasons"].([]any)[0].(map[string]any)["code"] != "funds" {
						t.Fatalf("lastConfirm=%v", last)
					}
				} else if value, exists := view["lastConfirm"]; !exists || value != nil {
					t.Fatalf("lastConfirm=%v exists=%t; want null", value, exists)
				}
				if trading {
					want := map[string]any{"connection": "paper", "venueAccount": "desk", "route": map[string]any{"timeInForce": "day"}}
					if !reflect.DeepEqual(view["destination"], want) {
						t.Fatalf("destination=%v", view["destination"])
					}
				} else if value, exists := view["destination"]; !exists || value != nil {
					t.Fatalf("destination=%v exists=%t; want null", value, exists)
				}
				svc.orderDetail.Order.Status = domain.OrderStatusCommitted
				rec = callTrading(t, svc, "/api/v1", http.MethodGet, "/orders/review-order", "")
				if rec.Code != 200 {
					t.Fatalf("non-waiting get status=%d: %s", rec.Code, rec.Body.String())
				}
				if _, exists := bodyMap(t, rec.Result())["review"]; exists {
					t.Fatal("non-waiting order must omit review")
				}
			})
		}
	}
}

func TestOrderReviewRESTGetMissingHistory(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*domain.OrderDetail)
	}{
		{"row", func(d *domain.OrderDetail) { d.Review = nil }},
		{"event", func(d *domain.OrderDetail) { d.Events = nil }},
		{"attestation", func(d *domain.OrderDetail) { d.Events[0].Attestation = nil }},
		{"envelope", func(d *domain.OrderDetail) { d.Events[0].Attestation.SignedEnvelope = "!bad" }},
		{"route", func(d *domain.OrderDetail) { d.Review.Destination = &domain.TradingDestination{Route: "invalid"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			detail := restReviewDetail(t)
			tc.change(&detail)
			rec := callTrading(t, &fakeService{orderDetail: detail}, "/api/v1", http.MethodGet, "/orders/review-order", "")
			if rec.Code != 500 {
				t.Fatalf("incomplete history status=%d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestOrderReviewRESTEmptyVenueAccount(t *testing.T) {
	detail := restReviewDetail(t)
	detail.Review.Destination = &domain.TradingDestination{Connection: "paper", Route: `{}`}
	rec := callTrading(t, &fakeService{orderDetail: detail}, "/api/v1", http.MethodGet, "/orders/review-order", "")
	if rec.Code != 200 {
		t.Fatalf("status=%d: %s", rec.Code, rec.Body.String())
	}
	view := bodyMap(t, rec.Result())["review"].(map[string]any)
	destination := view["destination"].(map[string]any)
	if value, exists := destination["venueAccount"]; !exists || value != "" {
		t.Fatalf("empty venue account must be retained: %v", destination)
	}
}

func TestOrderReviewOpenAPIContract(t *testing.T) {
	spec := string(openapiSpec)
	operations := map[string]string{}
	for _, op := range openAPIOperations(spec) {
		operations[op.Method+" "+op.Path] = strings.Join(op.Lines, "\n")
	}
	for _, route := range NewRouteRegistry(&fakeService{}, nil, noSealReport).Routes() {
		if !strings.Contains(route.Pattern, "/review/") {
			continue
		}
		operation, exists := operations[route.Method+" "+route.Pattern]
		if !exists {
			t.Fatalf("undocumented review route: %s %s", route.Method, route.Pattern)
		}
		if strings.Contains(operation, "requestBody:") {
			t.Fatalf("review route must have no body: %s", route.Pattern)
		}
		for _, status := range []string{"200", "400", "401", "403", "404", "409", "415", "422", "500", "503"} {
			if !strings.Contains(operation, `"`+status+`":`) {
				t.Fatalf("%s missing response %s", route.Pattern, status)
			}
		}
	}
	for _, schema := range []struct {
		name  string
		value any
	}{
		{"OrderReview", httpx.OrderReviewDTO[json.RawMessage]{}},
		{"BackupOrderReview", backup.OrderReview{}},
		{"ReviewLastConfirm", httpx.ReviewConfirmDTO{}},
		{"ConfirmOrderReviewResponse", confirmOrderReviewResponseDTO{}},
		{"EventReproductionResponse", eventReproductionResponseDTO{}},
		{"SignedApproval", signedApprovalDTO{}},
		{"AlertFailure", httpx.AlertFailureDTO{}},
		{"OrderMutationResponse", orderMutationResponseDTO{}},
		{"CheckResult", checkResultDTO{}},
	} {
		wantRequired, wantProperties := jsonMembers(reflect.TypeOf(schema.value))
		gotRequired, gotProperties := openAPISchemaMembers(t, spec, schema.name)
		if !reflect.DeepEqual(gotRequired, wantRequired) || !reflect.DeepEqual(gotProperties, wantProperties) {
			t.Fatalf("schema %s: required=%v properties=%v; DTO required=%v properties=%v", schema.name, gotRequired, gotProperties, wantRequired, wantProperties)
		}
	}
	schemas, ok := openAPIComponents(strings.Split(spec, "\n"), "schemas")
	if !ok {
		t.Fatal("OpenAPI schemas are missing")
	}
	if !strings.Contains(schemas["BackupData"], `
        orderReviews:
          type: array
          items:
            $ref: "#/components/schemas/BackupOrderReview"`) {
		t.Fatal("BackupData.orderReviews must be an array of BackupOrderReview")
	}
	if strings.Count(spec, "enum: [immediate, hold]") != 1 {
		t.Fatal("submission mode must be declared once in SubmitMode and referenced elsewhere")
	}
	if !strings.Contains(schemas["BackupOrderReview"], `
        mode:
          $ref: "#/components/schemas/SubmitMode"
`) {
		t.Fatal("BackupOrderReview.mode must reference SubmitMode")
	}
	if !strings.Contains(schemas["SubmitOrderRequest"], `
            mode:
              $ref: "#/components/schemas/SubmitMode"
`) {
		t.Fatal("SubmitOrderRequest.mode must reference SubmitMode")
	}
	if !strings.Contains(schemas["SubmitMode"]+"\n", "\n      enum: [immediate, hold]\n") {
		t.Fatal("SubmitMode must enumerate exactly immediate and hold")
	}
	if !strings.Contains(schemas["BackupOrderReview"], `
      dependentRequired:
        connection: [route]
        route: [connection]
        venueAccount: [connection]
      properties:`) {
		t.Fatal("BackupOrderReview connection and route must appear together, and venueAccount requires connection")
	}
	for _, name := range []string{"SignedApproval", "ConfirmOrderReviewResponse", "OrderMutationResponse"} {
		if !strings.Contains(schemas[name], "alertFailure:\n          $ref: \"#/components/schemas/AlertFailure\"") {
			t.Fatalf("%s must reference the shared AlertFailure schema", name)
		}
	}
	if !strings.Contains(spec, "SendVenueOrderResponse:\n      allOf:\n        - $ref: \"#/components/schemas/SignedApproval\"") {
		t.Fatal("venue sends must inherit SignedApproval, including alertFailure")
	}
}
