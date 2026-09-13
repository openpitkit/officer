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
	"context"
	"errors"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
)

func TestAttestationBlocksPreserveSDKPolicy(t *testing.T) {
	t.Parallel()
	const policy = domain.PolicySpotFundsPnlBoundsKillSwitch
	got := attestationBlocks([]domain.ExecutionAccountBlock{{
		Account: "acc-1", Policy: policy, Code: "pnl_bound_breached",
	}})
	if len(got) != 1 || got[0].Policy != policy {
		t.Fatalf("attestation blocks = %+v, want SDK policy %s", got, policy)
	}
}

// reachRecorder is a node that holds no order: it records that a cancel got as
// far as reading the order, and fails with an error that is not ErrInvalid.
type reachRecorder struct {
	node.Node
	reached bool
}

func (r *reachRecorder) GetOrder(context.Context, domain.ExternalID) (domain.OrderDetail, error) {
	r.reached = true
	return domain.OrderDetail{}, errors.New("no order")
}

// TestCancelOrderValidatesReason covers the free-form cancel reason: it is
// rendered verbatim into the audit detail line, so a newline could forge a
// second record and must be refused before the cancel reaches a node.
func TestCancelOrderValidatesReason(t *testing.T) {
	t.Parallel()

	const orderID = "ord-1"
	recorder := &reachRecorder{}
	svc := &Service{node: recorder}
	ctx := context.Background()

	_, _, err := svc.CancelOrder(ctx, orderID, "token", "",
		"typo\ncancel approval 00000000 order ord-2 reason=routine")
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("CancelOrder with a newline reason = %v, want ErrInvalid", err)
	}
	if recorder.reached {
		t.Fatal("cancel reached the node with an unvalidated reason")
	}

	_, _, err = svc.CancelOrder(ctx, orderID, "token", "", "typo")
	if errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("CancelOrder with a plain reason = %v, want it accepted", err)
	}
	if !recorder.reached {
		t.Fatal("cancel with a plain reason did not reach the node")
	}
}
