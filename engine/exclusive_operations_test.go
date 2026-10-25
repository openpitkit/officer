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
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	fwengine "go.openpit.dev/officer/framework/engine"
)

// TestExclusiveOperationsBesideOrderFlow moves one account between groups and
// retunes its SpotFunds bounds on the real engine while other accounts keep
// submitting orders through the node. Officer's lane gate interleaves the two,
// so this proves the composition neither deadlocks nor races and ends in the
// state the last move and retune set; TestSpotFundsRetuneWaitsForLaneWork and
// TestLocalNode_GroupOperationsWaitForLaneWork prove the exclusive sections.
func TestExclusiveOperationsBesideOrderFlow(t *testing.T) {
	ctx := context.Background()
	n, caller := newMissingAccountNode(t)
	for _, code := range []string{"grp-a", "grp-b"} {
		if _, err := n.CreateGroup(
			ctx, domain.AccountGroup{Code: code, Title: code}, caller,
		); err != nil {
			t.Fatalf("CreateGroup(%q): %v", code, err)
		}
	}
	const mover = domain.AccountID("mover")
	traders := []domain.AccountID{"trader-1", "trader-2", "trader-3"}
	fundMissingAccount(t, n, caller, mover)
	for _, trader := range traders {
		fundMissingAccount(t, n, caller, trader)
		// The first order publishes the traded assets before the flow starts.
		if _, err := n.SubmitOrder(
			ctx, missingAccountOrder(trader, "0.01"),
			domain.MissingAccountReject, caller,
		); err != nil {
			t.Fatalf("first SubmitOrder(%q): %v", trader, err)
		}
	}

	stop := make(chan struct{})
	errs := make(chan error, len(traders))
	var submitted atomic.Int64
	var wg sync.WaitGroup
	for _, trader := range traders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := n.SubmitOrder(
					ctx, missingAccountOrder(trader, "0.01"),
					domain.MissingAccountReject, caller,
				); err != nil {
					errs <- fmt.Errorf("SubmitOrder(%q): %w", trader, err)
					return
				}
				submitted.Add(1)
			}
		}()
	}

	groups := []string{"grp-a", "grp-b", ""}
	const rounds = 30
	for round := range rounds {
		group := groups[round%len(groups)]
		if err := n.SetAccountGroup(
			ctx, mover, group, domain.MissingAccountReject, caller,
		); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("round %d: SetAccountGroup(%q): %v", round, group, err)
		}
		if _, err := n.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
			Scope:      domain.ScopeAccount,
			Account:    mover,
			Currency:   "USDT",
			LowerBound: fmt.Sprintf("-%d", 100+round),
		}, domain.MissingAccountReject, caller); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("round %d: PutSpotFundsPnlBoundsLimit: %v", round, err)
		}
	}
	close(stop)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if submitted.Load() == 0 {
		t.Fatal("no order ran beside the exclusive operations")
	}

	stored, limits, err := n.GetAccountState(ctx, mover)
	if err != nil {
		t.Fatalf("GetAccountState(%q): %v", mover, err)
	}
	if want := groups[(rounds-1)%len(groups)]; stored.GroupCode != want {
		t.Fatalf("mover group = %q, want %q", stored.GroupCode, want)
	}
	if stored.Blocked {
		t.Fatalf("mover blocked after the moves: %+v", stored)
	}
	want := fmt.Sprintf("-%d", 100+rounds-1)
	found := false
	for _, limit := range limits.SpotFundsPnlBoundsLimits {
		if limit.Scope == domain.ScopeAccount && limit.LowerBound == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("mover SpotFunds bounds = %+v, want lower bound %s",
			limits.SpotFundsPnlBoundsLimits, want)
	}
}

// TestSpotFundsRetuneWaitsForLaneWork proves the SpotFunds retune runs in an
// exclusive section: while a task submitted straight to the dispatcher holds
// an account lane, the retune does not finish, and it completes once the lane
// is free.
func TestSpotFundsRetuneWaitsForLaneWork(t *testing.T) {
	acct := account(testAccount)
	acct.Currency = testQuote
	limits := []domain.LimitSpotFundsPnlBounds{{
		Scope:      domain.ScopeAccount,
		Account:    testAccount,
		Currency:   testQuote,
		LowerBound: "-3",
	}}
	built, err := newTestOpenPitEngineBuildFunc(t)(fwengine.Snapshot{
		Assets:                   testAssets(),
		Accounts:                 []domain.Account{acct},
		SpotFundsPnlBoundsLimits: limits,
	})
	if err != nil {
		t.Fatalf("NewOpenPitEngineBuildFunc: %v", err)
	}
	eng := built.(*openPitEngine)
	t.Cleanup(eng.Stop)
	source, err := eng.AccountID(testAccount)
	if err != nil {
		t.Fatalf("AccountID: %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	held := eng.AsyncEngine().Submit(
		context.Background(), source, func() error {
			close(started)
			<-release
			return nil
		},
	)
	<-started
	retuned := make(chan error, 1)
	go func() {
		_, err := eng.ConfigurePolicy(
			context.Background(),
			domain.PolicySpotFundsPnlBoundsKillSwitch,
			fwengine.LimitSet{SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{{
				Scope:      domain.ScopeAccount,
				Account:    testAccount,
				Currency:   testQuote,
				LowerBound: "-4",
			}}},
		)
		retuned <- err
	}()
	select {
	case err := <-retuned:
		close(release)
		t.Fatalf("retune finished while a lane was busy: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-retuned; err != nil {
		t.Fatalf("ConfigurePolicy after the lane freed: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := held.Await(waitCtx); err != nil {
		t.Fatalf("lane task: %v", err)
	}
}
