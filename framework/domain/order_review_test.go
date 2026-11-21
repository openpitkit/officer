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

package domain

import (
	"errors"
	"fmt"
	"slices"
	"testing"
)

func TestRequireOrderNotAwaitingReview(t *testing.T) {
	for _, tc := range []struct {
		status OrderStatus
		want   error
	}{
		{OrderStatusSubmitted, nil},
		{OrderStatusAccepted, nil},
		{OrderStatusRejected, nil},
		{OrderStatusCommitted, nil},
		{OrderStatusRolledBack, nil},
		{OrderStatusFilled, nil},
		{OrderStatusPartiallyFilled, nil},
		{OrderStatusCancelled, nil},
		{OrderStatusAwaitingReview, ErrConflict},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			err := RequireOrderNotAwaitingReview(tc.status)
			if !errors.Is(err, tc.want) {
				t.Fatalf("RequireOrderNotAwaitingReview = %v, want %v", err, tc.want)
			}
			if tc.want != nil && err.Error() != "order is awaiting review: conflict" {
				t.Fatalf("review error = %v, want awaiting review conflict", err)
			}
		})
	}
}

func TestRequireOrderModifiable(t *testing.T) {
	for _, status := range []OrderStatus{
		OrderStatusSubmitted, OrderStatusAccepted, OrderStatusCommitted,
		OrderStatusPartiallyFilled, OrderStatusRejected, OrderStatusRolledBack,
		OrderStatusFilled, OrderStatusCancelled, OrderStatusAwaitingReview,
	} {
		for _, force := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/force=%t", status, force), func(t *testing.T) {
				var want error
				if status == OrderStatusAwaitingReview {
					want = ErrConflict
				} else if OrderStatusTerminal(status) && !force {
					want = ErrTerminalOrder
				}
				err := RequireOrderModifiable(status, force)
				if !errors.Is(err, want) {
					t.Fatalf("RequireOrderModifiable = %v, want %v", err, want)
				}
				if status == OrderStatusAwaitingReview &&
					(errors.Is(err, ErrTerminalOrder) || err.Error() != "order is awaiting review: conflict") {
					t.Fatalf("review error = %v, want awaiting review conflict", err)
				}
			})
		}
	}
}

func TestOrderReviewStatus(t *testing.T) {
	status := OrderStatusAwaitingReview
	for _, tc := range []struct {
		name      string
		got, want bool
	}{
		{"supported", OrderStatusSupported(status), true},
		{"terminal", OrderStatusTerminal(status), false},
		{"eligible for fill", slices.Contains(OrderStatusesEligibleForFill(), status), false},
		{"active", slices.Contains(OrderStatusesActive(), status), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("got %v, want %v", tc.got, tc.want)
			}
		})
	}
	if event, ok := ExecutionReportStatusChangeEvent(status); ok || event != "" {
		t.Fatalf("review status maps to execution event %q", event)
	}
	if _, err := ExecutionReportRequiresEngine(ExecutionReportInput{OrderStatus: status}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("execution report review target error = %v, want ErrInvalid", err)
	}
}

func TestOrderReviewAuditDecision(t *testing.T) {
	for _, tc := range []struct {
		name    string
		action  AuditAction
		order   string
		verdict Verdict
		code    string
		invalid bool
	}{
		{"confirm accept", AuditActionConfirmOrderReview, "order", VerdictAccept, "", false},
		{"confirm accept with code", AuditActionConfirmOrderReview, "order", VerdictAccept, "limit", true},
		{"confirm reject", AuditActionConfirmOrderReview, "order", VerdictReject, "limit", false},
		{"confirm reject no code", AuditActionConfirmOrderReview, "order", VerdictReject, "", false},
		{"confirm review", AuditActionConfirmOrderReview, "order", VerdictReview, "limit", true},
		{"confirm missing order", AuditActionConfirmOrderReview, "", VerdictAccept, "", true},
		{"cancel", AuditActionCancelOrderReview, "", "", "", false},
		{"cancel decision", AuditActionCancelOrderReview, "order", VerdictAccept, "", true},
		{"cancel verdict only", AuditActionCancelOrderReview, "", VerdictReject, "", true},
		{"cancel code only", AuditActionCancelOrderReview, "", "", "limit", true},
		{"submit review", AuditActionSubmitOrder, "order", VerdictReview, "limit", false},
		{"drop copy review", AuditActionSubmitDropCopy, "order", VerdictReview, "limit", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAuditDecision(tc.action, tc.order, tc.verdict, tc.code)
			if (err != nil) != tc.invalid || (tc.invalid && !errors.Is(err, ErrInvalid)) {
				t.Fatalf("error = %v, invalid=%v", err, tc.invalid)
			}
		})
	}
	for _, action := range []AuditAction{AuditActionConfirmOrderReview, AuditActionCancelOrderReview} {
		if !slices.Contains(AllAuditActions(), action) || action.Category() != AuditCategoryControl {
			t.Fatalf("action %q missing or not control", action)
		}
	}
}
