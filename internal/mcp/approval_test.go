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

package mcp

import (
	"context"
	"errors"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openpit.dev/officer/internal/domain"
)

// approvalFakeSource wraps fakeSource with the three approval methods.
type approvalFakeSource struct {
	fakeSource

	submitResult SubmitOrderTokenResult
	submitErr    error
	submitCalls  []submitCall

	confirmOrder domain.Order
	confirmErr   error
	confirmCalls []confirmCall

	cancelOrder domain.Order
	cancelErr   error
	cancelCalls []cancelCallRecord
}

type submitCall struct {
	order domain.Order
	mode  string
}

type confirmCall struct {
	orderID int64
	token   string
}

type cancelCallRecord struct {
	orderID int64
	token   string
	reason  string
}

func (f *approvalFakeSource) SubmitOrderToken(
	_ context.Context, o domain.Order, mode string,
) (SubmitOrderTokenResult, error) {
	f.submitCalls = append(f.submitCalls, submitCall{order: o, mode: mode})
	return f.submitResult, f.submitErr
}

func (f *approvalFakeSource) ConfirmExecution(
	_ context.Context, orderID int64, token string,
) (domain.Order, error) {
	f.confirmCalls = append(f.confirmCalls, confirmCall{orderID: orderID, token: token})
	return f.confirmOrder, f.confirmErr
}

func (f *approvalFakeSource) CancelOrder(
	_ context.Context, orderID int64, token, reason string,
) (domain.Order, error) {
	f.cancelCalls = append(f.cancelCalls, cancelCallRecord{orderID: orderID, token: token, reason: reason})
	return f.cancelOrder, f.cancelErr
}

// helpers -----------------------------------------------------------------

func callSubmitOrder(
	t *testing.T, src Source, in submitOrderInput,
) *sdkmcp.CallToolResultFor[submitOrderOutput] {
	t.Helper()
	h := submitOrderHandler(src)
	res, err := h(context.Background(), nil,
		&sdkmcp.CallToolParamsFor[submitOrderInput]{Arguments: in})
	if err != nil {
		t.Fatalf("submitOrderHandler returned protocol error: %v", err)
	}
	return res
}

func callConfirmExecution(
	t *testing.T, src Source, in confirmExecutionInput,
) *sdkmcp.CallToolResultFor[confirmExecutionOutput] {
	t.Helper()
	h := confirmExecutionHandler(src)
	res, err := h(context.Background(), nil,
		&sdkmcp.CallToolParamsFor[confirmExecutionInput]{Arguments: in})
	if err != nil {
		t.Fatalf("confirmExecutionHandler returned protocol error: %v", err)
	}
	return res
}

func callCancel(
	t *testing.T, src Source, in cancelInput,
) *sdkmcp.CallToolResultFor[cancelOutput] {
	t.Helper()
	h := cancelHandler(src)
	res, err := h(context.Background(), nil,
		&sdkmcp.CallToolParamsFor[cancelInput]{Arguments: in})
	if err != nil {
		t.Fatalf("cancelHandler returned protocol error: %v", err)
	}
	return res
}

// gating tests ------------------------------------------------------------

// TestSubmitOrderGateMutating: submit_order returns a non-error disabled
// notice when the operator has turned the command off, and is not executed.
func TestSubmitOrderGateMutating(t *testing.T) {
	src := &approvalFakeSource{}
	src.disabledCommands = map[string]bool{submitOrderToolName: true}

	res := callSubmitOrder(t, src, submitOrderInput{
		Account: "acc1", BaseAsset: "BTC", QuoteAsset: "USD",
		Side: "buy", AmountKind: "quantity", AmountValue: "1",
	})

	if res.IsError {
		t.Fatalf("disabled gate must not be an error result, got IsError=true")
	}
	if len(src.submitCalls) != 0 {
		t.Fatalf("submit must not be called when gate is closed; got %d calls", len(src.submitCalls))
	}
}

// TestSubmitOrderGateMutatingAccessError: submit_order fails closed when the
// CommandEnabled check itself returns an error.
func TestSubmitOrderGateMutatingAccessError(t *testing.T) {
	src := &approvalFakeSource{}
	src.cmdEnabledErr = errors.New("store flake")

	res := callSubmitOrder(t, src, submitOrderInput{
		Account: "acc1", BaseAsset: "BTC", QuoteAsset: "USD",
		Side: "buy", AmountKind: "quantity", AmountValue: "1",
	})

	if !res.IsError {
		t.Fatalf("access-check error must produce IsError=true for mutating tool")
	}
	if len(src.submitCalls) != 0 {
		t.Fatalf("submit must not be called when gate errors; got %d calls", len(src.submitCalls))
	}
}

// TestConfirmExecutionGateMutating: confirm_execution returns disabled notice
// when operator has disabled the command.
func TestConfirmExecutionGateMutating(t *testing.T) {
	src := &approvalFakeSource{}
	src.disabledCommands = map[string]bool{confirmExecutionToolName: true}

	res := callConfirmExecution(t, src, confirmExecutionInput{OrderID: 1, Token: "tok"})

	if res.IsError {
		t.Fatalf("disabled gate must not be an error result")
	}
	if len(src.confirmCalls) != 0 {
		t.Fatalf("confirm must not be called when gate is closed")
	}
}

// TestConfirmExecutionGateMutatingAccessError: confirm_execution fails closed
// on access-check error.
func TestConfirmExecutionGateMutatingAccessError(t *testing.T) {
	src := &approvalFakeSource{}
	src.cmdEnabledErr = errors.New("store flake")

	res := callConfirmExecution(t, src, confirmExecutionInput{OrderID: 1, Token: "tok"})

	if !res.IsError {
		t.Fatalf("access-check error must produce IsError=true for mutating tool")
	}
}

// TestCancelGateMutating: cancel returns disabled notice when disabled.
func TestCancelGateMutating(t *testing.T) {
	src := &approvalFakeSource{}
	src.disabledCommands = map[string]bool{cancelToolName: true}

	res := callCancel(t, src, cancelInput{OrderID: 1, Token: "tok"})

	if res.IsError {
		t.Fatalf("disabled gate must not be an error result")
	}
	if len(src.cancelCalls) != 0 {
		t.Fatalf("cancel must not be called when gate is closed")
	}
}

// TestCancelGateMutatingAccessError: cancel fails closed on access-check error.
func TestCancelGateMutatingAccessError(t *testing.T) {
	src := &approvalFakeSource{}
	src.cmdEnabledErr = errors.New("store flake")

	res := callCancel(t, src, cancelInput{OrderID: 1, Token: "tok"})

	if !res.IsError {
		t.Fatalf("access-check error must produce IsError=true for mutating tool")
	}
}

// happy-path tests --------------------------------------------------------

// TestSubmitOrderHappyPath: submit_order forwards inputs and returns token
// fields when the gate is open.
func TestSubmitOrderHappyPath(t *testing.T) {
	expires := time.Now().UTC().Add(120 * time.Second)
	src := &approvalFakeSource{
		submitResult: SubmitOrderTokenResult{
			Token:     "eyFAKETOKEN",
			KeyID:     "key-1",
			ExpiresAt: expires,
			OrderID:   42,
		},
	}

	res := callSubmitOrder(t, src, submitOrderInput{
		Account:     "acc1",
		BaseAsset:   "BTC",
		QuoteAsset:  "USD",
		Side:        "buy",
		AmountKind:  "quantity",
		AmountValue: "0.5",
		Price:       "50000",
		Mode:        "hold",
	})

	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Content)
	}
	out := res.StructuredContent
	if out.Token != "eyFAKETOKEN" {
		t.Errorf("token: want %q got %q", "eyFAKETOKEN", out.Token)
	}
	if out.KeyID != "key-1" {
		t.Errorf("keyId: want key-1 got %q", out.KeyID)
	}
	if out.OrderID != 42 {
		t.Errorf("orderId: want 42 got %d", out.OrderID)
	}

	if len(src.submitCalls) != 1 {
		t.Fatalf("expected 1 submit call, got %d", len(src.submitCalls))
	}
	call := src.submitCalls[0]
	if call.order.Account != "acc1" {
		t.Errorf("account: want acc1 got %s", call.order.Account)
	}
	if call.mode != "hold" {
		t.Errorf("mode: want hold got %s", call.mode)
	}
}

// TestSubmitOrderMissingAccount: submit_order returns error when account is empty.
func TestSubmitOrderMissingAccount(t *testing.T) {
	src := &approvalFakeSource{}

	res := callSubmitOrder(t, src, submitOrderInput{
		BaseAsset: "BTC", QuoteAsset: "USD", Side: "buy",
		AmountKind: "quantity", AmountValue: "1",
	})

	if !res.IsError {
		t.Fatalf("want IsError=true for missing account")
	}
}

// TestSubmitOrderBackendError: submit_order returns error when backend fails.
func TestSubmitOrderBackendError(t *testing.T) {
	src := &approvalFakeSource{
		submitErr: errors.New("engine reject"),
	}

	res := callSubmitOrder(t, src, submitOrderInput{
		Account: "acc1", BaseAsset: "BTC", QuoteAsset: "USD",
		Side: "buy", AmountKind: "quantity", AmountValue: "1",
	})

	if !res.IsError {
		t.Fatalf("want IsError=true when backend returns error")
	}
}

// TestConfirmExecutionHappyPath: confirm_execution forwards orderId+token and
// returns order status.
func TestConfirmExecutionHappyPath(t *testing.T) {
	src := &approvalFakeSource{
		confirmOrder: domain.Order{ID: 7, Status: domain.OrderStatusCommitted},
	}

	res := callConfirmExecution(t, src, confirmExecutionInput{OrderID: 7, Token: "tok-abc"})

	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Content)
	}
	out := res.StructuredContent
	if out.OrderID != 7 {
		t.Errorf("orderId: want 7 got %d", out.OrderID)
	}
	if out.Status != string(domain.OrderStatusCommitted) {
		t.Errorf("status: want committed got %q", out.Status)
	}

	if len(src.confirmCalls) != 1 {
		t.Fatalf("expected 1 confirm call, got %d", len(src.confirmCalls))
	}
	if src.confirmCalls[0].token != "tok-abc" {
		t.Errorf("token: want tok-abc got %q", src.confirmCalls[0].token)
	}
}

// TestConfirmExecutionMissingToken: confirm_execution returns error when token
// is empty.
func TestConfirmExecutionMissingToken(t *testing.T) {
	src := &approvalFakeSource{}

	res := callConfirmExecution(t, src, confirmExecutionInput{OrderID: 7})

	if !res.IsError {
		t.Fatalf("want IsError=true for missing token")
	}
}

// TestConfirmExecutionMissingOrderID: confirm_execution returns error when
// orderId is zero.
func TestConfirmExecutionMissingOrderID(t *testing.T) {
	src := &approvalFakeSource{}

	res := callConfirmExecution(t, src, confirmExecutionInput{Token: "tok"})

	if !res.IsError {
		t.Fatalf("want IsError=true for missing orderId")
	}
}

// TestCancelHappyPath: cancel forwards inputs and returns order status.
func TestCancelHappyPath(t *testing.T) {
	src := &approvalFakeSource{
		cancelOrder: domain.Order{ID: 9, Status: domain.OrderStatusRolledBack},
	}

	res := callCancel(t, src, cancelInput{OrderID: 9, Token: "tok-xyz", Reason: "operator"})

	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Content)
	}
	out := res.StructuredContent
	if out.OrderID != 9 {
		t.Errorf("orderId: want 9 got %d", out.OrderID)
	}
	if out.Status != string(domain.OrderStatusRolledBack) {
		t.Errorf("status: want rolled_back got %q", out.Status)
	}

	if len(src.cancelCalls) != 1 {
		t.Fatalf("expected 1 cancel call, got %d", len(src.cancelCalls))
	}
	c := src.cancelCalls[0]
	if c.reason != "operator" {
		t.Errorf("reason: want operator got %q", c.reason)
	}
}

// TestCancelMissingToken: cancel returns error when token is empty.
func TestCancelMissingToken(t *testing.T) {
	src := &approvalFakeSource{}

	res := callCancel(t, src, cancelInput{OrderID: 9})

	if !res.IsError {
		t.Fatalf("want IsError=true for missing token")
	}
}

// TestCancelBackendError: cancel returns error when backend fails.
func TestCancelBackendError(t *testing.T) {
	src := &approvalFakeSource{
		cancelErr: errors.New("already rolled back"),
	}

	res := callCancel(t, src, cancelInput{OrderID: 9, Token: "tok"})

	if !res.IsError {
		t.Fatalf("want IsError=true when backend returns error")
	}
}
