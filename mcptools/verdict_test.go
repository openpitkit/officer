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

package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	fwsigning "go.openpit.dev/officer/framework/signing"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

type reviewToolSource struct {
	testPolicyWireSource
	verdict domain.Verdict
	reviews []domain.ReviewMark
	send    *backend.VenueSend
	check   domain.CheckResult
	detail  domain.OrderDetail
}

func (s reviewToolSource) CheckOrder(context.Context, domain.OrderProbe) (domain.CheckResult, error) {
	return s.check, nil
}

func (s reviewToolSource) GetOrder(context.Context, string) (domain.OrderDetail, error) {
	return s.detail, nil
}

func reviewToolMarks() []domain.ReviewMark {
	return []domain.ReviewMark{
		{OrderReject: domain.OrderReject{Code: "size", Scope: "order", Policy: "size", Reason: "review size", Details: "limit"}, Mode: domain.RuleMode("review_without_hold")},
		{OrderReject: domain.OrderReject{Code: "rate", Scope: "account", Policy: "rate", Reason: "review rate", Details: "window"}, Mode: domain.RuleMode("review_without_hold")},
	}
}

func requireReviewToolMarks(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var marks []httpx.ReviewMarkDTO
	if err := json.Unmarshal(raw, &marks); err != nil {
		t.Fatal(err)
	}
	want := []httpx.ReviewMarkDTO{
		{OrderRejectDTO: httpx.OrderRejectDTO{Code: "size", Scope: "order", Policy: "size", Reason: "review size", Details: "limit"}, Mode: "review_without_hold"},
		{OrderRejectDTO: httpx.OrderRejectDTO{Code: "rate", Scope: "account", Policy: "rate", Reason: "review rate", Details: "window"}, Mode: "review_without_hold"},
	}
	if !reflect.DeepEqual(marks, want) {
		t.Fatalf("review marks = %+v; want %+v", marks, want)
	}
}

func reviewToolJSON(t *testing.T, value any) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func callReviewTool(t *testing.T, src reviewToolSource, name string, args any, wantText string, wantError bool, wantTextParts ...string) map[string]json.RawMessage {
	t.Helper()
	reg := frameworkmcp.NewToolRegistry()
	RegisterTools(reg, nil)
	session := testPolicySession(t, reg, src)
	result, err := session.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError != wantError {
		t.Fatalf("%s IsError = %v; want %v; content = %+v", name, result.IsError, wantError, result.Content)
	}
	if len(result.Content) != 1 {
		t.Fatalf("%s content = %+v; want one text item", name, result.Content)
	}
	text, ok := result.Content[0].(*sdkmcp.TextContent)
	if !ok {
		t.Fatalf("%s content = %+v; want text %q", name, result.Content[0], wantText)
	}
	if wantError {
		if text.Text != wantText {
			t.Fatalf("%s text = %q; want %q", name, text.Text, wantText)
		}
	} else {
		summary, payload, found := strings.Cut(text.Text, "\n")
		if !found || summary != wantText {
			t.Fatalf("%s summary = %q; want %q followed by JSON", name, summary, wantText)
		}
		var textOutput any
		if err := json.Unmarshal([]byte(payload), &textOutput); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(textOutput, result.StructuredContent) {
			t.Fatalf("%s text JSON = %+v; structuredContent = %+v", name, textOutput, result.StructuredContent)
		}
		for _, part := range wantTextParts {
			if !strings.Contains(text.Text, part) {
				t.Fatalf("%s text = %q; want literal %q", name, text.Text, part)
			}
		}
		list, err := session.ListTools(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var outputSchema any
		for _, tool := range list.Tools {
			if tool.Name == name {
				outputSchema = tool.OutputSchema
				break
			}
		}
		if outputSchema == nil {
			t.Fatalf("%s has no published output schema", name)
		}
		rawSchema, err := json.Marshal(outputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(rawSchema, &schema); err != nil {
			t.Fatal(err)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := resolved.Validate(result.StructuredContent); err != nil {
			t.Fatalf("%s structuredContent violates published output schema: %v", name, err)
		}
	}
	return reviewToolJSON(t, result.StructuredContent)
}

func (s reviewToolSource) SubmitOrder(context.Context, domain.Order, string, domain.MissingAccountPolicy) (frameworkmcp.SubmitOrderResult, error) {
	return frameworkmcp.SubmitOrderResult{
		OrderExternalID: "review-order", Verdict: s.verdict, Reviews: s.reviews,
	}, nil
}

func (s reviewToolSource) SendPaperOrder(context.Context, domain.Order, backend.SendDestination) (backend.VenueSendResult, error) {
	return backend.VenueSendResult{
		Approval: backend.SignedApproval{
			OrderExternalID: "review-order", Verdict: s.verdict, Reviews: s.reviews,
		},
		Send: s.send,
	}, nil
}

func TestSubmitOrderReviewText(t *testing.T) {
	src := reviewToolSource{verdict: domain.VerdictReview, reviews: reviewToolMarks()}
	want := "order review-order waits for operator review - 2 reason(s); do not send"
	raw := callReviewTool(t, src, "submit_order", submitOrderInput{
		Account: "desk", BaseAsset: "AAPL", QuoteAsset: "USD", Side: "buy",
		AmountKind: "quantity", AmountValue: "1", Mode: "hold", MissingAccount: "reject",
	}, want, false)
	if string(raw["verdict"]) != `"review"` {
		t.Fatalf("submit_order verdict = %s; want review", raw["verdict"])
	}
	requireReviewToolMarks(t, raw["reviews"])
	if _, ok := raw["venueSend"]; ok {
		t.Fatal("submit_order exposed venueSend")
	}
}

func TestSendPaperOrderReviewText(t *testing.T) {
	src := reviewToolSource{
		verdict: domain.VerdictReview, reviews: reviewToolMarks(),
		send: &backend.VenueSend{Status: "must-not-be-shown"},
	}
	want := "order review-order waits for operator review - 2 reason(s); do not send"
	raw := callReviewTool(t, src, "send_paper_order", sendVenueOrderInput{
		Account: "desk", BaseAsset: "AAPL", QuoteAsset: "USD", Side: "buy",
		AmountKind: "quantity", AmountValue: "1", Destination: sendDestinationInput{Route: map[string]any{}},
	}, want, false)
	if string(raw["verdict"]) != `"review"` {
		t.Fatalf("send_paper_order verdict = %s; want review", raw["verdict"])
	}
	requireReviewToolMarks(t, raw["reviews"])
	if _, ok := raw["venueSend"]; ok {
		t.Fatal("send_paper_order exposed venueSend")
	}
}

func TestCheckOrderReview(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result domain.CheckResult
		text   string
	}{
		{"review", domain.CheckResult{Passed: true, Reviews: reviewToolMarks()}, "check desk: review - 2 reason(s); do not send"},
		{"pass", domain.CheckResult{Passed: true}, "check desk: pass"},
		{"reject", domain.CheckResult{Rejects: []domain.OrderReject{{Code: "size"}}}, "check desk: reject - 1 reason(s)"},
		{"reject with marks", domain.CheckResult{Rejects: []domain.OrderReject{{Code: "size"}}, Reviews: reviewToolMarks()}, "check desk: reject - 1 reason(s)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := callReviewTool(t, reviewToolSource{check: tc.result}, "check_order", checkOrderInput{
				Account: "desk", BaseAsset: "AAPL", QuoteAsset: "USD", Side: "buy",
				AmountKind: "quantity", AmountValue: "1",
			}, tc.text, false)
			var passed bool
			if err := json.Unmarshal(raw["passed"], &passed); err != nil {
				t.Fatal(err)
			}
			if passed != tc.result.Passed {
				t.Fatalf("check_order passed = %v; want %v", passed, tc.result.Passed)
			}
			if len(tc.result.Reviews) > 0 {
				requireReviewToolMarks(t, raw["reviews"])
				var marks []map[string]string
				if err := json.Unmarshal(raw["reviews"], &marks); err != nil {
					t.Fatal(err)
				}
				if len(marks) != 2 || marks[0]["mode"] != "review_without_hold" || marks[0]["code"] != "size" || len(marks[0]) != 6 {
					t.Fatalf("review mark JSON = %s", raw["reviews"])
				}
			} else if _, ok := raw["reviews"]; ok {
				t.Fatal("check_order exposed empty reviews")
			}
		})
	}
}

func reviewToolDetail(t *testing.T) domain.OrderDetail {
	t.Helper()
	token, err := fwsigning.BuildEnvelope(fwsigning.Envelope{Approval: domain.ApprovalPayload{
		Verdict: domain.VerdictReview, Reviews: reviewToolMarks(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return domain.OrderDetail{
		Order:  domain.Order{ExternalID: "review-order", Status: domain.OrderStatusAwaitingReview},
		Review: &domain.OrderReview{Mode: domain.SubmitModeHold},
		Events: []domain.OrderEvent{{Type: domain.OrderEventPreTradeReview,
			Attestation: &domain.EventAttestation{SignedEnvelope: token, RequestType: domain.AttestationRequestSubmit},
			Payload:     domain.OrderEventPayload{Rejects: []domain.OrderReject{{Code: "not-a-mark"}}},
		}},
	}
}

func TestGetOrderReview(t *testing.T) {
	for _, trading := range []bool{false, true} {
		for _, confirmed := range []bool{false, true} {
			name := "gateway"
			if trading {
				name = "trading"
			}
			if confirmed {
				name += "/rejected-confirm"
			}
			t.Run(name, func(t *testing.T) {
				detail := reviewToolDetail(t)
				var wantTextParts []string
				if trading {
					detail.Review.Destination = &domain.TradingDestination{
						Connection: "connection-1", VenueAccount: "venue-account",
						Route: `{"timeInForce":"day","large":9007199254740993,"nested":{"venue":"X","price":1.234567890123456789}}`,
					}
					wantTextParts = []string{"9007199254740993", "1.234567890123456789"}
					if err := domain.ValidateTradingRoute(detail.Review.Destination.Route); err != nil {
						t.Fatal(err)
					}
				}
				at := time.Date(2026, 11, 19, 10, 20, 30, 123456789, time.UTC)
				if confirmed {
					for i, code := range []string{"old", "latest"} {
						detail.Events = append(detail.Events, domain.OrderEvent{
							Type: domain.OrderEventPreTradeRejected, At: at.Add(time.Duration(i) * time.Second),
							Attestation: &domain.EventAttestation{RequestType: domain.AttestationRequestReviewConfirm},
							Payload:     domain.OrderEventPayload{Rejects: []domain.OrderReject{{Code: code, Scope: "order", Policy: "size", Reason: "rejected", Details: "limit"}}},
						})
					}
				}
				out := callReviewTool(t, reviewToolSource{detail: detail}, "get_order", getOrderInput{OrderExternalID: "review-order"},
					"order review-order: awaiting_review (0 fill(s))", false, wantTextParts...)
				var raw map[string]json.RawMessage
				if err := json.Unmarshal(out["review"], &raw); err != nil {
					t.Fatal(err)
				}
				if len(raw) != 3 || raw["marks"] == nil || raw["lastConfirm"] == nil || raw["destination"] == nil {
					t.Fatalf("review keys = %v; want exactly marks, lastConfirm, destination", raw)
				}
				requireReviewToolMarks(t, raw["marks"])
				var review httpx.OrderReviewDTO[map[string]any]
				if err := json.Unmarshal(out["review"], &review); err != nil {
					t.Fatal(err)
				}
				if confirmed {
					want := &httpx.ReviewConfirmDTO{Verdict: "reject", Reasons: []httpx.OrderRejectDTO{{Code: "latest", Scope: "order", Policy: "size", Reason: "rejected", Details: "limit"}}, At: at.Add(time.Second).Format(time.RFC3339Nano)}
					if !reflect.DeepEqual(review.LastConfirm, want) {
						t.Fatalf("lastConfirm = %+v; want %+v", review.LastConfirm, want)
					}
				} else if string(raw["lastConfirm"]) != "null" {
					t.Fatalf("lastConfirm = %s; want null", raw["lastConfirm"])
				}
				if trading {
					dest := review.Destination
					wantRoute := map[string]any{"timeInForce": "day", "large": float64(9007199254740993), "nested": map[string]any{"venue": "X", "price": float64(1.234567890123456789)}}
					if dest == nil || dest.Connection != "connection-1" || dest.VenueAccount != "venue-account" || !reflect.DeepEqual(dest.Route, wantRoute) {
						t.Fatalf("destination = %+v; want stored destination", dest)
					}
				} else if string(raw["destination"]) != "null" {
					t.Fatalf("destination = %s; want null", raw["destination"])
				}
			})
		}
	}
}

func TestGetOrderWithoutReview(t *testing.T) {
	detail := reviewToolDetail(t)
	detail.Order.Status = domain.OrderStatusCommitted
	detail.Review = nil
	out := callReviewTool(t, reviewToolSource{detail: detail}, "get_order", getOrderInput{OrderExternalID: "review-order"},
		"order review-order: committed (0 fill(s))", false)
	if _, ok := out["review"]; ok {
		t.Fatal("non-waiting order exposed review")
	}
}

func TestGetOrderReviewApproval(t *testing.T) {
	for _, tc := range []struct {
		name         string
		confirm      []domain.Verdict
		status       domain.OrderStatus
		wantApproval domain.Verdict
	}{
		{"waiting", nil, domain.OrderStatusAwaitingReview, domain.VerdictReview},
		{"rejected-confirm", []domain.Verdict{domain.VerdictReject}, domain.OrderStatusAwaitingReview, domain.VerdictReject},
		{"rejected-then-accepted-confirm", []domain.Verdict{domain.VerdictReject, domain.VerdictAccept}, domain.OrderStatusCommitted, domain.VerdictAccept},
	} {
		t.Run(tc.name, func(t *testing.T) {
			detail := reviewToolDetail(t)
			envelopes := map[domain.Verdict]string{domain.VerdictReview: detail.Events[0].Attestation.SignedEnvelope}
			at := time.Date(2026, 11, 19, 10, 20, 30, 0, time.UTC)
			detail.Events[0].At = at
			for i, verdict := range tc.confirm {
				token, err := fwsigning.BuildEnvelope(fwsigning.Envelope{Approval: domain.ApprovalPayload{Verdict: verdict}})
				if err != nil {
					t.Fatal(err)
				}
				envelopes[verdict] = token
				eventType := domain.OrderEventPreTradeRejected
				if verdict == domain.VerdictAccept {
					eventType = domain.OrderEventPreTradeAccepted
				}
				detail.Events = append(detail.Events, domain.OrderEvent{
					Type: eventType, At: at.Add(-time.Duration(i+1) * time.Second),
					Attestation: &domain.EventAttestation{SignedEnvelope: token, RequestType: domain.AttestationRequestReviewConfirm},
				})
			}
			detail.Order.Status = tc.status
			if tc.status != domain.OrderStatusAwaitingReview {
				detail.Review = nil
			}
			out := callReviewTool(t, reviewToolSource{detail: detail}, "get_order", getOrderInput{OrderExternalID: "review-order"},
				fmt.Sprintf("order review-order: %s (0 fill(s))", tc.status), false)
			var approval orderApprovalDTO
			if err := json.Unmarshal(out["approval"], &approval); err != nil {
				t.Fatal(err)
			}
			if approval.SignedEnvelope != envelopes[tc.wantApproval] {
				t.Fatalf("approval envelope = %q; want %s envelope %q", approval.SignedEnvelope, tc.wantApproval, envelopes[tc.wantApproval])
			}
		})
	}
}

func TestGetOrderApprovalFallback(t *testing.T) {
	first := &domain.EventAttestation{SignedEnvelope: "first", RequestType: domain.AttestationRequestExecutionReport}
	accepted := &domain.EventAttestation{SignedEnvelope: "accepted", RequestType: domain.AttestationRequestExecutionReport}
	rejected := &domain.EventAttestation{SignedEnvelope: "rejected", RequestType: domain.AttestationRequestExecutionReport}
	for _, tc := range []struct {
		name   string
		events []domain.OrderEvent
		want   string
	}{
		{"first-accept-reject", []domain.OrderEvent{
			{Type: domain.OrderEventPreTradeReview, Attestation: first},
			{Type: domain.OrderEventPreTradeAccepted, Attestation: accepted},
			{Type: domain.OrderEventPreTradeRejected, Attestation: rejected},
		}, "accepted"},
		{"first-attested", []domain.OrderEvent{
			{Type: domain.OrderEventPreTradeAccepted},
			{Type: domain.OrderEventPreTradeReview, Attestation: first},
		}, "first"},
		{"unattested", []domain.OrderEvent{{Type: domain.OrderEventPreTradeAccepted}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			detail := domain.OrderDetail{
				Order:  domain.Order{ExternalID: "review-order", Status: domain.OrderStatusCommitted},
				Events: tc.events,
			}
			out := callReviewTool(t, reviewToolSource{detail: detail}, "get_order", getOrderInput{OrderExternalID: "review-order"},
				"order review-order: committed (0 fill(s))", false)
			var approval *orderApprovalDTO
			if err := json.Unmarshal(out["approval"], &approval); err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if approval != nil {
					t.Fatalf("approval = %+v; want null", approval)
				}
			} else if approval == nil || approval.SignedEnvelope != tc.want {
				t.Fatalf("approval = %+v; want envelope %q", approval, tc.want)
			}
		})
	}
}

func TestGetOrderApprovalIgnoresExecutionReport(t *testing.T) {
	submitEnvelope, err := fwsigning.BuildEnvelope(fwsigning.Envelope{Approval: domain.ApprovalPayload{
		Verdict: domain.VerdictAccept, RequestType: string(domain.AttestationRequestSubmit), Mode: domain.SubmitModeHold,
	}})
	if err != nil {
		t.Fatal(err)
	}
	reportEnvelope, err := fwsigning.BuildEnvelope(fwsigning.Envelope{Approval: domain.ApprovalPayload{
		Verdict: domain.VerdictAccept, RequestType: string(domain.AttestationRequestExecutionReport), Mode: domain.SubmitModeHold,
	}})
	if err != nil {
		t.Fatal(err)
	}
	detail := domain.OrderDetail{
		Order: domain.Order{ExternalID: "review-order", Status: domain.OrderStatusCommitted},
		Events: []domain.OrderEvent{
			{Type: domain.OrderEventPreTradeAccepted, Attestation: &domain.EventAttestation{
				SignedEnvelope: submitEnvelope, RequestType: domain.AttestationRequestSubmit,
			}},
			{Type: domain.OrderEventPreTradeAccepted, Attestation: &domain.EventAttestation{
				SignedEnvelope: reportEnvelope, RequestType: domain.AttestationRequestExecutionReport,
			}},
		},
	}
	out := callReviewTool(t, reviewToolSource{detail: detail}, "get_order", getOrderInput{OrderExternalID: "review-order"},
		"order review-order: committed (0 fill(s))", false)
	var approval orderApprovalDTO
	if err := json.Unmarshal(out["approval"], &approval); err != nil {
		t.Fatal(err)
	}
	if approval.SignedEnvelope != submitEnvelope {
		t.Fatalf("approval envelope = %q; want submit envelope %q", approval.SignedEnvelope, submitEnvelope)
	}
}

func TestGetOrderReviewBrokenHistory(t *testing.T) {
	for _, tc := range []struct {
		name        string
		breakDetail func(*domain.OrderDetail)
	}{
		{"missing row", func(d *domain.OrderDetail) { d.Review = nil }},
		{"missing event", func(d *domain.OrderDetail) { d.Events = nil }},
		{"missing attestation", func(d *domain.OrderDetail) { d.Events[0].Attestation = nil }},
		{"invalid envelope", func(d *domain.OrderDetail) { d.Events[0].Attestation.SignedEnvelope = "broken" }},
		{"duplicate event", func(d *domain.OrderDetail) { d.Events = append(d.Events, d.Events[0]) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			detail := reviewToolDetail(t)
			tc.breakDetail(&detail)
			callReviewTool(t, reviewToolSource{detail: detail}, "get_order", getOrderInput{OrderExternalID: "review-order"},
				"internal: internal error", true)
		})
	}
}

func TestGetOrderReviewInvalidRoute(t *testing.T) {
	for _, route := range []string{`null`, `[]`, `"route"`, `{} {}`} {
		t.Run(route, func(t *testing.T) {
			detail := reviewToolDetail(t)
			detail.Review.Destination = &domain.TradingDestination{Connection: "connection-1", VenueAccount: "venue-account", Route: route}
			callReviewTool(t, reviewToolSource{detail: detail}, "get_order", getOrderInput{OrderExternalID: "review-order"},
				"internal: internal error", true)
		})
	}
}

func TestVerdictToolsRejectUnknownAndMissingSend(t *testing.T) {
	for _, verdict := range []domain.Verdict{"", "unknown"} {
		src := reviewToolSource{verdict: verdict}
		callReviewTool(t, src, "submit_order", submitOrderInput{
			Account: "desk", BaseAsset: "AAPL", QuoteAsset: "USD", Side: "buy",
			AmountKind: "quantity", AmountValue: "1", Mode: "hold", MissingAccount: "reject",
		}, fmt.Sprintf("unknown order verdict %q", verdict), true)
		callReviewTool(t, src, "send_paper_order", sendVenueOrderInput{
			Account: "desk", BaseAsset: "AAPL", QuoteAsset: "USD", Side: "buy",
			AmountKind: "quantity", AmountValue: "1", Destination: sendDestinationInput{Route: map[string]any{}},
		}, fmt.Sprintf("unknown order verdict %q", verdict), true)
	}
	callReviewTool(t, reviewToolSource{verdict: domain.VerdictAccept}, "send_paper_order", sendVenueOrderInput{
		Account: "desk", BaseAsset: "AAPL", QuoteAsset: "USD", Side: "buy",
		AmountKind: "quantity", AmountValue: "1", Destination: sendDestinationInput{Route: map[string]any{}},
	}, "accepted order has no venue send result", true)
}
