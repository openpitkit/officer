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
	"testing"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
)

func seedPositionLimitNode(t *testing.T, n node.Node, caller domain.Caller) domain.LimitPosition {
	t.Helper()
	ctx := context.Background()
	limit := positionLimit("position-acc", "BTC", "100")
	if _, err := n.PutPositionLimit(ctx, limit, domain.LimitWriteReplace, domain.MissingAccountCreate, caller); err != nil {
		t.Fatal(err)
	}
	fundMissingAccount(t, n, caller, limit.Account)
	if _, err := n.ApplyAdjustment(ctx, limit.Account, "", domain.AdjustmentRequest{
		Type: domain.AdjustmentTypeDeposit, Asset: "BTC",
		Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: "90"},
	}, domain.MissingAccountReject, caller); err != nil {
		t.Fatal(err)
	}
	return limit
}

func checkNodePositionLimit(t *testing.T, n node.Node, quantity string, passed bool) {
	t.Helper()
	o := missingAccountOrder("position-acc", quantity)
	out, err := n.CheckOrder(context.Background(), domain.OrderProbe{
		Account: o.Account, BaseAsset: o.BaseAsset, QuoteAsset: o.QuoteAsset,
		Side: o.Side, AmountKind: o.AmountKind, AmountValue: o.AmountValue, Price: o.Price,
	})
	if err != nil || out.Passed != passed {
		t.Fatalf("position check %s = %+v, %v; want passed=%t", quantity, out, err, passed)
	}
	if out.WouldBlock != nil {
		t.Fatalf("position check blocked account: %+v", out.WouldBlock)
	}
	if passed {
		if len(out.Rejects) != 0 {
			t.Fatalf("passing position check has rejects: %+v", out.Rejects)
		}
	} else if len(out.Rejects) != 1 || out.Rejects[0].Code != domain.RejectCodePositionLimitExceeded {
		t.Fatalf("position check rejects = %+v, want %s", out.Rejects, domain.RejectCodePositionLimitExceeded)
	}
}

func TestPositionLimitNodeBoundaryOpenOrderChangeAndClear(t *testing.T) {
	n, caller := newMissingAccountNode(t)
	limit := seedPositionLimitNode(t, n, caller)
	checkNodePositionLimit(t, n, "10", true)
	checkNodePositionLimit(t, n, "10.00000001", false)
	checkNodePositionLimit(t, n, "20", false)
	checkNodePositionLimit(t, n, "10", true)
	if order, err := n.SubmitOrder(context.Background(), missingAccountOrder(limit.Account, "5"), domain.MissingAccountReject, caller); err != nil || order.Status != domain.OrderStatusCommitted {
		t.Fatalf("commit buy of 5 = %+v, %v", order, err)
	}
	checkNodePositionLimit(t, n, "5.00000001", false)
	checkNodePositionLimit(t, n, "5", true)
	limit.MaxPosition = "120"
	if _, err := n.PutPositionLimit(context.Background(), limit, domain.LimitWriteReplace, domain.MissingAccountReject, caller); err != nil {
		t.Fatal(err)
	}
	checkNodePositionLimit(t, n, "25", true)
	checkNodePositionLimit(t, n, "25.00000001", false)
	if _, err := n.DeleteLimit(context.Background(), node.LimitTarget{
		Policy: domain.PolicyPositionLimit, Scope: limit.Scope, Account: limit.Account, Asset: limit.Asset,
	}, caller); err != nil {
		t.Fatal(err)
	}
	checkNodePositionLimit(t, n, "25.00000001", true)
}

func TestPositionLimitNodeRestart(t *testing.T) {
	path := t.TempDir() + "/officer.db"
	n, closeNode := openMissingAccountNode(t, path)
	caller := domain.Caller{Source: domain.SourcePanel, Principal: domain.PrincipalOperator}
	seedPositionLimitNode(t, n, caller)
	checkNodePositionLimit(t, n, "10.00000001", false)
	closeNode()
	n, _ = openMissingAccountNode(t, path)
	checkNodePositionLimit(t, n, "10", true)
	checkNodePositionLimit(t, n, "10.00000001", false)
}

func TestPositionLimitNodeBackupRestore(t *testing.T) {
	ctx := context.Background()
	n, caller := newMissingAccountNode(t)
	limit := seedPositionLimitNode(t, n, caller)
	archive, err := n.ExportBackup(ctx, backup.Scope{All: true}, caller)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.DeleteLimit(ctx, node.LimitTarget{
		Policy: domain.PolicyPositionLimit, Scope: limit.Scope, Account: limit.Account, Asset: limit.Asset,
	}, caller); err != nil {
		t.Fatal(err)
	}
	checkNodePositionLimit(t, n, "20", true)
	if summary, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite,
	}, caller); err != nil || summary.RestartRequired {
		t.Fatalf("RestoreBackup = %+v, %v, want online restore", summary, err)
	}
	checkNodePositionLimit(t, n, "10", true)
	checkNodePositionLimit(t, n, "10.00000001", false)
}
