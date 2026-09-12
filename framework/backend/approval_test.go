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

package backend

import (
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
)

// TestBuildExecutionReportPayloadSignsWriteSetLeaves covers the builder alone:
// the signed result binds the exact leaves carried by the write set.
func TestBuildExecutionReportPayloadSignsWriteSetLeaves(t *testing.T) {
	t.Parallel()
	// The builder must use the write set rather than an older order snapshot.
	order := domain.Order{
		Account: "acc-1",
		Leaves:  "0",
		Status:  domain.OrderStatusCancelled,
	}
	persistence := engine.ExecutionReportPersistence{
		OrderStatus: domain.OrderStatusCancelled,
		Leaves:      "5",
	}

	payload, err := (&Service{}).buildExecutionReportPayload(order, persistence)
	if err != nil {
		t.Fatalf("buildExecutionReportPayload: %v", err)
	}
	if payload.Result == nil {
		t.Fatal("payload.Result is nil, want an execution-report result")
	}
	if payload.Result.LeavesQuantity != "5" {
		t.Fatalf(
			"signed leaves = %q, want the venue-reported 5",
			payload.Result.LeavesQuantity,
		)
	}
	if payload.Result.LeavesQuantity == order.Leaves {
		t.Fatal("signed leaves is the durable order quantity, not the report's")
	}
}

func TestBuildExecutionReportPayloadCarriesTypedBlockCause(t *testing.T) {
	t.Parallel()
	block := domain.ExecutionAccountBlock{
		Account: "acc-1", Policy: "SpotFundsPolicy",
		Code: domain.RejectCodePnlKillSwitchTriggered, Reason: "lower bound breached",
		Details: "account pnl below lower bound",
	}
	payload, err := (&Service{}).buildExecutionReportPayload(
		domain.Order{Account: block.Account},
		engine.ExecutionReportPersistence{Blocks: []domain.ExecutionAccountBlock{block}},
	)
	if err != nil {
		t.Fatalf("buildExecutionReportPayload: %v", err)
	}
	if payload.RejectPolicy != block.Policy || payload.RejectCode != block.Code ||
		payload.RejectReason != block.Reason || payload.RejectDetails != block.Details {
		t.Fatalf("signed block cause = %+v, want %+v", payload, block)
	}
}

// TestBuildImmediateSettlementPayloadSignsWriteSetLeaves exercises the
// assembly site the immediate submit signs through.
func TestBuildImmediateSettlementPayloadSignsWriteSetLeaves(t *testing.T) {
	t.Parallel()
	event := domain.OrderEvent{
		Type: domain.OrderEventFill,
		Payload: domain.OrderEventPayload{
			LeavesQuantity: "5",
			OrderStatus:    string(domain.OrderStatusFilled),
			ExecutionReport: &domain.ExecutionReportRequest{
				LeavesQuantity: "5",
				OrderStatus:    domain.OrderStatusFilled,
			},
		},
	}
	order := domain.Order{
		Account: "acc-1",
		Leaves:  "0",
		Status:  domain.OrderStatusFilled,
	}
	persistence := engine.ExecutionReportPersistence{
		OrderStatus: domain.OrderStatusFilled,
		Leaves:      "5",
	}

	payload, err := (&Service{}).buildImmediateSettlementPayload(
		order, event, &persistence,
	)
	if err != nil {
		t.Fatalf("buildImmediateSettlementPayload: %v", err)
	}
	if payload.Result == nil {
		t.Fatal("payload.Result is nil, want an execution-report result")
	}
	if payload.Result.LeavesQuantity != "5" {
		t.Fatalf(
			"signed leaves = %q, want the venue-reported 5",
			payload.Result.LeavesQuantity,
		)
	}
	if payload.RequestType != string(domain.AttestationRequestExecutionReport) {
		t.Fatalf(
			"request type = %q, want %s",
			payload.RequestType,
			domain.AttestationRequestExecutionReport,
		)
	}
	if _, err := (&Service{}).buildImmediateSettlementPayload(
		order, event, nil,
	); err == nil {
		t.Fatal("settlement without a write set: want an error")
	}
}
