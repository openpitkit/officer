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
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	fwengine "go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/openpit/accountadjustment"
	"go.openpit.dev/openpit/asyncengine"
	"go.openpit.dev/openpit/configure"
	"go.openpit.dev/openpit/param"
	"go.openpit.dev/openpit/pretrade"
	"go.openpit.dev/openpit/reject"
)

func assertCountingLimitOrder(
	t *testing.T, eng fwengine.Engine, order domain.Order, want domain.Verdict,
	mode domain.RuleMode, policy, code string,
) {
	t.Helper()
	out, err := materializeOrderResult(eng, order)
	if err != nil || out.Verdict != want {
		t.Fatalf("order result = %+v, %v; want %s", out, err, want)
	}
	switch want {
	case domain.VerdictReview:
		if len(out.Reviews) != 1 || out.Reviews[0].Mode != mode ||
			out.Reviews[0].Policy != policy || out.Reviews[0].Code != code {
			t.Fatalf("review marks = %+v, want one %s %s %s mark", out.Reviews, policy, mode, code)
		}
	case domain.VerdictReject:
		if !hasRejectCode(out.Rejects, code) {
			t.Fatalf("rejects = %+v, want %s", out.Rejects, code)
		}
	case domain.VerdictAccept:
		if len(out.Reviews) != 0 || len(out.Rejects) != 0 {
			t.Fatalf("accepted order carries marks: %+v", out)
		}
	}
}

func TestClassifyConfigureError(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		kind configure.ErrorKind
	}{
		{name: "validation", kind: configure.ErrorKindValidation},
		{name: "unknown", kind: configure.ErrorKindUnknown},
		{name: "type_mismatch", kind: configure.ErrorKindTypeMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sdkErr := &configure.Error{Kind: tc.kind, Message: "SDK refused the threshold"}
			original := fmt.Errorf("engine: configure rate_limit: %w", sdkErr)
			got := classifyConfigureError(original)
			if tc.kind != configure.ErrorKindValidation {
				if got != original || errors.Is(got, domain.ErrInvalid) {
					t.Fatalf("classifyConfigureError = %v, want original error unchanged", got)
				}
				return
			}
			if !errors.Is(got, domain.ErrInvalid) || !errors.Is(got, original) {
				t.Fatalf("classifyConfigureError = %v, want both the original error and ErrInvalid", got)
			}
			var preserved *configure.Error
			if !errors.As(got, &preserved) || preserved != sdkErr {
				t.Fatalf("classifyConfigureError = %v, want the same SDK error", got)
			}
			want := original.Error() + ": " + domain.ErrInvalid.Error()
			if got.Error() != want {
				t.Fatalf("error message = %q, want %q", got.Error(), want)
			}
		})
	}
	for _, err := range []error{
		nil, errors.New("SDK refused the threshold"), domain.ErrNotImplemented, context.Canceled,
	} {
		if got := classifyConfigureError(err); got != err {
			t.Errorf("classifyConfigureError(%v) = %v, want unchanged", err, got)
		}
	}
}

func TestConfigurePolicy_RateSDKValidation(t *testing.T) {
	t.Parallel()
	limit := rateLimit(domain.ScopeBroker, "", "", 1, time.Hour)
	snap := orderSizeInstanceSnapshot()
	snap.RateLimits = []domain.LimitRate{limit}
	eng, err := newTestOpenPitEngineBuildFunc(t)(snap)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Stop()

	limit.Window = 0
	_, err = eng.ConfigurePolicy(context.Background(), domain.PolicyRateLimit,
		fwengine.LimitSet{RateLimits: []domain.LimitRate{limit}})
	requireSDKConfigureValidation(t, err, "rate limit window must be positive")

	order := domain.Order{Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1", Price: "1"}
	assertCountingLimitOrder(t, eng, order, domain.VerdictAccept, limit.Mode, nameRateLimit, "rate_limit_exceeded")
	assertCountingLimitOrder(t, eng, order, domain.VerdictReject, limit.Mode, nameRateLimit, "rate_limit_exceeded")
}

func TestConfigurePolicy_SpotFundsPnlBoundsSDKValidation(t *testing.T) {
	t.Parallel()
	for _, axis := range []domain.SpotFundsPnlAxis{
		domain.SpotFundsPnlAxisRealized, domain.SpotFundsPnlAxisUnrealized, domain.SpotFundsPnlAxisTotal,
	} {
		t.Run(string(axis), func(t *testing.T) {
			limit := pnlModeLimit(domain.RuleModeReviewWithoutHold, domain.ScopeAccount, "-3")
			limit.Axis = axis
			if axis == domain.SpotFundsPnlAxisUnrealized {
				limit.LowerBound = ""
				limit.UpperBound = "-3"
			}
			snap := pnlModeSnapshot("-5")
			snap.SpotFundsPnlBoundsLimits = []domain.LimitSpotFundsPnlBounds{limit}
			eng, err := newTestOpenPitEngineBuildFunc(t)(snap)
			if err != nil {
				t.Fatal(err)
			}
			defer eng.Stop()

			refused := limit
			refused.Mode = domain.RuleModeReject
			_, err = eng.ConfigurePolicy(context.Background(), axis.Policy(),
				fwengine.LimitSet{SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{refused, refused}})
			requireSDKConfigureValidation(t, err, "duplicate account P&L barrier")

			order := testOrder()
			order.AmountValue = "1"
			assertCountingLimitOrder(t, eng, order, domain.VerdictReview, limit.Mode, nameSpotFunds, domain.RejectCodePnlKillSwitchTriggered)
		})
	}
}

func requireSDKConfigureValidation(t *testing.T, err error, message string) {
	t.Helper()
	var sdkErr *configure.Error
	if !errors.As(err, &sdkErr) || sdkErr.Kind != configure.ErrorKindValidation {
		t.Fatalf("ConfigurePolicy error = %v, want SDK validation error", err)
	}
	if !strings.Contains(sdkErr.Message, message) || !strings.Contains(err.Error(), sdkErr.Message) {
		t.Fatalf("ConfigurePolicy error = %v, SDK message = %q, want SDK text containing %q", err, sdkErr.Message, message)
	}
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ConfigurePolicy error = %v, want ErrInvalid", err)
	}
}

func TestRateLimit_RuleModes(t *testing.T) {
	t.Parallel()
	for _, mode := range []domain.RuleMode{
		domain.RuleModeReject, domain.RuleModeReviewWithHold, domain.RuleModeReviewWithoutHold,
	} {
		for _, runtime := range []bool{false, true} {
			path := "build"
			if runtime {
				path = "runtime"
			}
			t.Run(string(mode)+"/"+path, func(t *testing.T) {
				limit := rateLimit(domain.ScopeBroker, "", "", 1, time.Hour)
				limit.Mode = mode
				snap := orderSizeInstanceSnapshot()
				snap.RateLimits = []domain.LimitRate{limit}
				if runtime {
					snap.RateLimits = []domain.LimitRate{rateLimit(domain.ScopeBroker, "", "", 100, time.Hour)}
				}
				eng, err := newTestOpenPitEngineBuildFunc(t)(snap)
				if err != nil {
					t.Fatal(err)
				}
				defer eng.Stop()
				if runtime {
					if _, err := eng.ConfigurePolicy(context.Background(), domain.PolicyRateLimit,
						fwengine.LimitSet{RateLimits: []domain.LimitRate{limit}}); err != nil {
						t.Fatal(err)
					}
				}
				order := domain.Order{Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
					Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1", Price: "1"}
				assertCountingLimitOrder(t, eng, order, domain.VerdictAccept, mode, nameRateLimit, "rate_limit_exceeded")
				want := domain.VerdictReview
				if mode == domain.RuleModeReject {
					want = domain.VerdictReject
				}
				assertCountingLimitOrder(t, eng, order, want, mode, nameRateLimit, "rate_limit_exceeded")
			})
		}
	}
}

func TestRateLimit_ModesCoexist(t *testing.T) {
	t.Parallel()
	for _, scope := range []struct {
		scope   domain.LimitScope
		account string
		asset   string
	}{
		{domain.ScopeBroker, "", ""},
		{domain.ScopeAsset, "", "USD"},
		{domain.ScopeAccount, "acc-1", ""},
		{domain.ScopeAccountAsset, "acc-1", "USD"},
	} {
		for _, mode := range []domain.RuleMode{domain.RuleModeReviewWithHold, domain.RuleModeReviewWithoutHold} {
			for _, runtime := range []bool{false, true} {
				path := "build"
				if runtime {
					path = "runtime"
				}
				t.Run(string(scope.scope)+"/"+string(mode)+"/"+path, func(t *testing.T) {
					rejectLimit := rateLimit(scope.scope, scope.account, scope.asset, 3, time.Hour)
					reviewLimit := rateLimit(scope.scope, scope.account, scope.asset, 1, time.Hour)
					reviewLimit.Mode = mode
					limits := []domain.LimitRate{rejectLimit, reviewLimit}
					snap := orderSizeInstanceSnapshot()
					snap.RateLimits = limits
					if runtime {
						snap.RateLimits = []domain.LimitRate{rejectLimit}
					}
					eng, err := newTestOpenPitEngineBuildFunc(t)(snap)
					if err != nil {
						t.Fatal(err)
					}
					defer eng.Stop()
					if runtime {
						if _, err := eng.ConfigurePolicy(context.Background(), domain.PolicyRateLimit,
							fwengine.LimitSet{RateLimits: limits}); err != nil {
							t.Fatal(err)
						}
					}
					adapter := eng.(*openPitEngine)
					if len(adapter.registered) != 2 {
						t.Fatalf("instances = %+v, want SpotFunds and one RateLimit", adapter.registered)
					}
					order := domain.Order{Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
						Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1", Price: "1"}
					assertCountingLimitOrder(t, eng, order, domain.VerdictAccept, mode, nameRateLimit, "rate_limit_exceeded")
					assertCountingLimitOrder(t, eng, order, domain.VerdictReview, mode, nameRateLimit, "rate_limit_exceeded")
					limits[0].MaxOrders = 1
					if _, err := eng.ConfigurePolicy(context.Background(), domain.PolicyRateLimit,
						fwengine.LimitSet{RateLimits: limits}); err != nil {
						t.Fatal(err)
					}
					assertCountingLimitOrder(t, eng, order, domain.VerdictReject, mode, nameRateLimit, "rate_limit_exceeded")
					invalid := append(append([]domain.LimitRate{}, limits...), limits[1])
					if _, err := eng.ConfigurePolicy(context.Background(), domain.PolicyRateLimit,
						fwengine.LimitSet{RateLimits: invalid}); err == nil {
						t.Fatal("duplicate mode accepted")
					}
					assertCountingLimitOrder(t, eng, order, domain.VerdictReject, mode, nameRateLimit, "rate_limit_exceeded")
				})
			}
		}
	}
}

type reviewOperationResult struct {
	t      *testing.T
	marks  []reject.Reject
	err    error
	output *heldOperationResult
}

type heldOperationResult struct {
	marks       []reject.Reject
	lock        pretrade.Lock
	adjustments []accountadjustment.Outcome
}

func (r heldOperationResult) Reviews() ([]reject.Reject, error) { return r.marks, nil }
func (r heldOperationResult) Lock() (pretrade.Lock, error)      { return r.lock, nil }
func (r heldOperationResult) AccountAdjustments() ([]accountadjustment.Outcome, error) {
	return r.adjustments, nil
}

func TestOrderModelHeldFlags(t *testing.T) {
	e := newTestEngine(t)
	if _, err := e.OrderModel(testOrder(), fwengine.ReviewFlags{AlreadyHeld: true}); err == nil || !strings.Contains(err.Error(), "requires Confirmed") {
		t.Fatalf("AlreadyHeld without Confirmed error = %v, want explicit error", err)
	}
	order, err := e.OrderModel(testOrder(), fwengine.ReviewFlags{Confirmed: true, AlreadyHeld: true})
	if err != nil {
		t.Fatal(err)
	}
	review, ok := order.Review().Get()
	if !ok {
		t.Fatal("confirmed already-held order has no review group")
	}
	confirmed, confirmedSet := review.Confirmed().Get()
	held, heldSet := review.AlreadyHeld().Get()
	if !confirmedSet || !confirmed || !heldSet || !held {
		t.Fatalf("review flags = %+v, want Confirmed and AlreadyHeld", review.Values())
	}
}

func TestForcedHeldReservationValidation(t *testing.T) {
	e := newTestEngine(t)
	price, err := param.NewPriceFromString(testLimit)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := pretrade.NewLockFromEntries([]pretrade.Entry{{Price: price}})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := marshalLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		held fwengine.HeldReservation
		want string
	}{
		{"lock", fwengine.HeldReservation{Quantity: testQty}, "lock is empty"},
		{"quantity", fwengine.HeldReservation{Lock: stored}, "quantity is invalid"},
		{"zero", fwengine.HeldReservation{Lock: stored, Quantity: "0"}, "quantity is zero"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.ForcedHeldOrder(testOrder(), tc.held)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("forced hold error = %v, want %q", err, tc.want)
			}
			_, err = e.PrepareForcedHeldImmediate(testOrder(), tc.held)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("forced immediate error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestHeldReservationValidation(t *testing.T) {
	e := newTestEngine(t)
	price, err := param.NewPriceFromString(testLimit)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := pretrade.NewLockFromEntries([]pretrade.Entry{{Price: price}})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := marshalLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	held := fwengine.HeldReservation{Lock: stored, Quantity: testQty}
	for _, tc := range []struct {
		name   string
		held   fwengine.HeldReservation
		result heldOperationResult
		want   string
	}{
		{"missing lock", fwengine.HeldReservation{Quantity: testQty}, heldOperationResult{lock: pretrade.NewLock()}, "held reservation lock is empty"},
		{"missing quantity", fwengine.HeldReservation{Lock: stored}, heldOperationResult{lock: pretrade.NewLock()}, "held reservation quantity is invalid"},
		{"decimal zero quantity", fwengine.HeldReservation{Lock: stored, Quantity: "0.0"}, heldOperationResult{lock: pretrade.NewLock()}, "held reservation quantity is zero"},
		{"negative quantity", fwengine.HeldReservation{Lock: stored, Quantity: "-1"}, heldOperationResult{lock: pretrade.NewLock()}, "held reservation quantity is invalid"},
		{"review marks", held, heldOperationResult{lock: pretrade.NewLock(), marks: []reject.Reject{{Policy: "unexpected"}}}, "returned review marks"},
		{"lock", held, heldOperationResult{lock: lock}, "returned a non-empty lock"},
		{"adjustments", held, heldOperationResult{lock: pretrade.NewLock(), adjustments: []accountadjustment.Outcome{{}}}, "returned account adjustments"},
		{"invalid stored lock", fwengine.HeldReservation{Lock: []byte("invalid"), Quantity: testQty}, heldOperationResult{lock: pretrade.NewLock()}, "unmarshal lock"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.ReservedHeldOrder(testOrder(), tc.result, tc.held)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ReservedHeldOrder error = %v, want %q", err, tc.want)
			}
			_, err = e.PrepareImmediateHeldReservation(testOrder(), tc.result, tc.held)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("PrepareImmediateHeldReservation error = %v, want %q", err, tc.want)
			}
		})
	}
}

func (r reviewOperationResult) Reviews() ([]reject.Reject, error) {
	return r.marks, r.err
}

func (r reviewOperationResult) Lock() (pretrade.Lock, error) {
	if r.output != nil {
		return r.output.lock, nil
	}
	r.t.Fatal("review verdict read Lock")
	return pretrade.Lock{}, nil
}

func (r reviewOperationResult) AccountAdjustments() ([]accountadjustment.Outcome, error) {
	if r.output != nil {
		return r.output.adjustments, nil
	}
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
		held  bool
	}{
		{name: "without hold", modes: []reject.RuleMode{reject.RuleModeReviewWithoutHold}, want: []domain.ReviewMark{want}},
		{name: "mixed", modes: []reject.RuleMode{reject.RuleModeReviewWithHold, reject.RuleModeReviewWithoutHold}, want: []domain.ReviewMark{
			{OrderReject: want.OrderReject, Mode: domain.RuleModeReviewWithHold}, want,
		}},
		{name: "with hold", modes: []reject.RuleMode{reject.RuleModeReviewWithHold}, held: true, want: []domain.ReviewMark{
			{OrderReject: want.OrderReject, Mode: domain.RuleModeReviewWithHold},
		}},
		{name: "reject mark", modes: []reject.RuleMode{reject.RuleModeReject}, err: "reject mode in review marks"},
		{name: "unknown mode", modes: []reject.RuleMode{255}, err: "unknown rule mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			marks := make([]reject.Reject, len(tc.modes))
			for i, mode := range tc.modes {
				marks[i] = mark
				marks[i].Mode = mode
			}
			operation := reviewOperationResult{t: t, marks: marks}
			if tc.held {
				operation.output = &heldOperationResult{lock: pretrade.NewLock()}
			}
			result, err := e.ReservedOrder(testOrder(), operation)
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
			if tc.held {
				expected.Held = true
				expected.Outcomes = []fwengine.BalanceOutcome{}
				expected.Blocks = []domain.AccountBlock{}
				expected.Lock, _, err = capturePreTradeOutput(operation.output.lock, testOrder())
				if err != nil {
					t.Fatal(err)
				}
			}
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
