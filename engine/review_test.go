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

package engine

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	fwengine "go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/openpit/accountadjustment"
	"go.openpit.dev/openpit/asyncengine"
	"go.openpit.dev/openpit/pretrade"
	"go.openpit.dev/openpit/reject"
)

type reviewOperationResult struct {
	t     *testing.T
	marks []reject.Reject
	err   error
}

func (r reviewOperationResult) Reviews() ([]reject.Reject, error) {
	return r.marks, r.err
}

func (r reviewOperationResult) Lock() (pretrade.Lock, error) {
	r.t.Fatal("review verdict read Lock")
	return pretrade.Lock{}, nil
}

func (r reviewOperationResult) AccountAdjustments() ([]accountadjustment.Outcome, error) {
	r.t.Fatal("review verdict read AccountAdjustments")
	return nil, nil
}

func TestReservedOrderReview(t *testing.T) {
	e := newTestEngine(t)
	mark := reject.Reject{
		Code: reject.CodeSystemUnavailable, Scope: reject.ScopeAccount,
		Policy: "test-review", Reason: " review\x00 required ",
		Details: " asset 2: pending\x00 ", Mode: reject.RuleModeReviewWithoutHold,
	}
	want := domain.ReviewMark{
		OrderReject: domain.OrderReject{
			Code: "system_unavailable", Scope: "account", Policy: "test-review",
			Reason: "review required", Details: "asset \"USD\": pending",
		},
		Mode: domain.RuleModeReviewWithoutHold,
	}
	for _, tc := range []struct {
		name  string
		modes []reject.RuleMode
		want  []domain.ReviewMark
		err   string
	}{
		{name: "without hold", modes: []reject.RuleMode{reject.RuleModeReviewWithoutHold}, want: []domain.ReviewMark{want}},
		{name: "mixed", modes: []reject.RuleMode{reject.RuleModeReviewWithHold, reject.RuleModeReviewWithoutHold}, want: []domain.ReviewMark{
			{OrderReject: want.OrderReject, Mode: domain.RuleModeReviewWithHold}, want,
		}},
		{name: "with hold", modes: []reject.RuleMode{reject.RuleModeReviewWithHold}, err: "review with hold is not supported"},
		{name: "reject mark", modes: []reject.RuleMode{reject.RuleModeReject}, err: "reject mode in review marks"},
		{name: "unknown mode", modes: []reject.RuleMode{255}, err: "unknown rule mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			marks := make([]reject.Reject, len(tc.modes))
			for i, mode := range tc.modes {
				marks[i] = mark
				marks[i].Mode = mode
			}
			result, err := e.ReservedOrder(testOrder(), reviewOperationResult{t: t, marks: marks})
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("ReservedOrder error = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			expected := fwengine.OrderResult{Verdict: domain.VerdictReview, Reviews: tc.want}
			if !reflect.DeepEqual(result, expected) {
				t.Fatalf("ReservedOrder = %+v, want review marks only: %+v", result, expected)
			}
		})
	}

	prepared, err := e.PrepareImmediateReservation(domain.Order{}, reviewOperationResult{t: t, marks: []reject.Reject{mark}})
	if err != nil {
		t.Fatal(err)
	}
	expected := fwengine.ImmediatePreparation{Verdict: domain.VerdictReview, Reviews: []domain.ReviewMark{want}}
	if !reflect.DeepEqual(prepared, expected) {
		t.Fatalf("PrepareImmediateReservation = %+v, want review marks only: %+v", prepared, expected)
	}
	for _, verdict := range []domain.Verdict{domain.VerdictReject, domain.VerdictReview, "", "unknown"} {
		_, err := e.SettleImmediate(domain.Order{}, fwengine.ImmediatePreparation{Verdict: verdict}, pretrade.PostTradeResult{})
		if err == nil {
			t.Fatalf("SettleImmediate(%q) succeeded", verdict)
		}
	}
	readErr := errors.New("review read failed")
	if _, err := e.ReservedOrder(testOrder(), reviewOperationResult{t: t, err: readErr}); !errors.Is(err, readErr) {
		t.Fatalf("ReservedOrder review read error = %v, want %v", err, readErr)
	}
	if _, err := e.PrepareImmediateReservation(domain.Order{}, reviewOperationResult{t: t, err: readErr}); !errors.Is(err, readErr) {
		t.Fatalf("PrepareImmediateReservation review read error = %v, want %v", err, readErr)
	}
}

type reviewCheckResult struct {
	asyncengine.OrderCheckResult
	marks []reject.Reject
}

func (r reviewCheckResult) Reviews() ([]reject.Reject, error) { return r.marks, nil }

func TestCheckedOrderReview(t *testing.T) {
	e := newTestEngine(t)
	probe := domain.OrderProbe{
		Account: testAccount, BaseAsset: testBase, QuoteAsset: testQuote,
		Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity,
		AmountValue: testQty, Price: testLimit,
	}
	source, err := e.CheckOrderModel(probe)
	if err != nil {
		t.Fatal(err)
	}
	mark := reject.Reject{
		Code: reject.CodeSystemUnavailable, Scope: reject.ScopeOrder,
		Reason: "review", Mode: reject.RuleModeReviewWithoutHold,
	}
	ctx := context.Background()
	chain := asyncengine.Chain(source, func(context.Context) (struct{}, error) {
		return struct{}{}, nil
	}).CheckOrder(func(_ context.Context, _ struct{}, checked asyncengine.OrderCheckResult) error {
		result, err := e.CheckedOrder(probe, reviewCheckResult{OrderCheckResult: checked, marks: []reject.Reject{mark}})
		if err != nil {
			return err
		}
		want := []domain.ReviewMark{{
			OrderReject: domain.OrderReject{Code: "system_unavailable", Scope: "order", Reason: "review"},
			Mode:        domain.RuleModeReviewWithoutHold,
		}}
		if !result.Passed || len(result.Rejects) != 0 || !reflect.DeepEqual(result.Reviews, want) {
			t.Fatalf("CheckedOrder = %+v, want passed with mapped reviews", result)
		}
		return nil
	})
	if _, err := chain.Run(ctx, e.AsyncEngine()).Await(ctx); err != nil {
		t.Fatal(err)
	}
}
