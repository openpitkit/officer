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

package native

import (
	"context"
	"errors"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/internal/store/sqlite"
)

func TestLocalNode_ApplyAdjustmentRejectsAverageEntryPriceOnly(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(t.TempDir() + "/officer.db")
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	n, _, err := node.NewLocalNode(
		ctx,
		store,
		func(snapshot engine.Snapshot) (engine.Engine, error) {
			return BuildOpenPitEngine("", snapshot)
		},
	)
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })

	caller := domain.Caller{
		Source:    domain.SourcePanel,
		Principal: domain.PrincipalOperator,
	}
	if _, err := n.CreateAccount(ctx, domain.Account{Code: testAccount, Currency: testQuote}, caller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	_, err = n.ApplyAdjustment(
		ctx,
		node.Key{Account: testAccount},
		domain.ExternalID(""),
		domain.AdjustmentRequest{Asset: testQuote, AverageEntryPrice: "1"},
		caller,
	)
	if !errors.Is(err, domain.ErrNoChange) {
		t.Fatalf("ApplyAdjustment: %v, want ErrNoChange", err)
	}

	balance, ok, err := n.GetBalance(ctx, testAccount, testQuote)
	if err != nil || ok {
		t.Fatalf("GetBalance: ok=%v err=%v balance=%+v, want missing", ok, err, balance)
	}
}
