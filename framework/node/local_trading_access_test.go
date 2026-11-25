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

package node

import (
	"context"
	"errors"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

type committedAccessRealm struct {
	store.RealmStore
	cancel   context.CancelFunc
	auditErr error
}

func (r committedAccessRealm) PutTradingAccess(ctx context.Context, access domain.TradingAccess) error {
	if err := r.RealmStore.PutTradingAccess(ctx, access); err != nil {
		return err
	}
	r.cancel()
	return nil
}

func (r committedAccessRealm) AppendAudit(ctx context.Context, entry store.AuditEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.auditErr != nil {
		return r.auditErr
	}
	return r.RealmStore.AppendAudit(ctx, entry)
}

func TestTradingAccessObserverWiringFailuresAreInternal(t *testing.T) {
	n := &localNode{}
	if err := n.InstallTradingAccessObserver(nil); err == nil || errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("missing observer = %v, want internal error", err)
	}
	observer := func(context.Context, domain.Account, domain.Caller) error { return nil }
	if err := n.InstallTradingAccessObserver(observer); err != nil {
		t.Fatal(err)
	}
	if err := n.InstallTradingAccessObserver(observer); err == nil || errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("duplicate observer = %v, want internal error", err)
	}
}

func TestTradingAccessNotificationCompletesOutsideNodeLocks(t *testing.T) {
	n, realm, paper := newTradingFeeAccountNode(t)
	ctx := context.Background()
	live, err := realm.CreateTradingConnection(ctx, domain.TradingConnection{ExternalID: "live", Provider: domain.TradingProviderAlpaca, Label: "live", Mode: domain.TradingModeLive, VenueIdentity: "live", Credentials: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.PutTradingAccess(ctx, domain.TradingAccess{Account: "first", Connection: paper.ExternalID}, false, testCaller); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	if err := n.InstallTradingAccessObserver(func(context.Context, domain.Account, domain.Caller) error {
		close(entered)
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	defer func() {
		close(release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	go func() {
		done <- n.PutTradingAccess(ctx, domain.TradingAccess{Account: "first", Connection: live.ExternalID}, false, testCaller)
	}()
	<-entered
	if !n.mutate.TryLock() {
		t.Fatal("delivery holds node mutation lock")
	}
	n.mutate.Unlock()
	if !n.laneGate.TryLock() {
		t.Fatal("delivery holds node lane gate")
	}
	n.laneGate.Unlock()
	if !n.engineMu.TryLock() {
		t.Fatal("delivery holds engine lock")
	}
	n.engineMu.Unlock()
	if n.restarting.Load() {
		t.Fatal("delivery holds restart reservation")
	}
	select {
	case err := <-done:
		done <- err // The deferred check receives the result.
		t.Fatalf("notification returned before delivery completed: %v", err)
	default:
	}
}

func TestTradingAccessCommittedCancellationAndAuditFailureStillNotify(t *testing.T) {
	for _, auditFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "canceled", true: "audit-failed"}[auditFailure], func(t *testing.T) {
			n, realm, paper := newTradingFeeAccountNode(t)
			live, err := realm.CreateTradingConnection(t.Context(), domain.TradingConnection{ExternalID: "live", Provider: domain.TradingProviderAlpaca, Label: "live", Mode: domain.TradingModeLive, VenueIdentity: "live", Credentials: `{}`})
			if err != nil {
				t.Fatal(err)
			}
			if err := n.PutTradingAccess(t.Context(), domain.TradingAccess{Account: "first", Connection: paper.ExternalID}, false, testCaller); err != nil {
				t.Fatal(err)
			}
			auditErr, deliveryErr := errors.New("audit failed"), errors.New("delivery failed")
			calls := 0
			if err := n.InstallTradingAccessObserver(func(ctx context.Context, account domain.Account, caller domain.Caller) error {
				calls++
				if ctx.Err() != nil || account.Code != "first" || caller != testCaller {
					t.Errorf("committed notification context/identity lost")
				}
				return deliveryErr
			}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			wrapped := committedAccessRealm{RealmStore: realm, cancel: cancel}
			if auditFailure {
				wrapped.auditErr = auditErr
			}
			n.realm = wrapped
			err = n.PutTradingAccess(ctx, domain.TradingAccess{Account: "first", Connection: live.ExternalID}, false, testCaller)
			if calls != 1 || !errors.Is(err, deliveryErr) || !errors.Is(err, domain.ErrTradingAccessNotification) || errors.Is(err, context.Canceled) {
				t.Fatalf("committed completion calls=%d error=%v", calls, err)
			}
			if errors.Is(err, auditErr) != auditFailure || domain.OnlyTradingAccessNotification(err) == auditFailure {
				t.Fatalf("joined audit classification=%v", err)
			}
			if err := n.PutTradingAccess(t.Context(), domain.TradingAccess{Account: "first", Connection: live.ExternalID}, false, testCaller); errors.Is(err, domain.ErrTradingAccessNotification) || calls != 1 {
				t.Fatalf("already mixed emitted another warning: calls=%d err=%v", calls, err)
			}
		})
	}
}
