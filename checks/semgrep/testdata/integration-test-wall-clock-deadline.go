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

package testdata

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func requestUnderDeadline(t *testing.T) {
	// ruleid: integration-test-wall-clock-deadline
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = ctx
}

func requestUnderFixedDeadline(t *testing.T) {
	// ruleid: integration-test-wall-clock-deadline
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Second))
	defer cancel()
	_ = ctx
}

func waitUnderBudget(t *testing.T, entered chan struct{}) {
	// ruleid: integration-test-wall-clock-deadline
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("did not enter")
	}
}

func waitUnderFormattedBudget(t *testing.T, entered chan struct{}) {
	// ruleid: integration-test-wall-clock-deadline
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatalf("did not enter in %s", time.Second)
	}
}

func receiveUnderBudget(t *testing.T, result <-chan error) {
	// ruleid: integration-test-wall-clock-deadline
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("did not receive result")
	}
}

func assignUnderBudget(t *testing.T, result <-chan error) {
	var err error
	// ruleid: integration-test-wall-clock-deadline
	select {
	case err = <-result:
		t.Log(err)
	case <-time.After(time.Second):
		t.Fatalf("did not receive result in %s", time.Second)
	}
}

func requestUnderTest(t *testing.T) {
	// ok: integration-test-wall-clock-deadline
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_ = ctx
}

func waitForState(entered chan struct{}) {
	// ok: integration-test-wall-clock-deadline
	<-entered
}

func proveAbsence(t *testing.T, entered chan struct{}) {
	// ok: integration-test-wall-clock-deadline
	select {
	case <-entered:
		t.Fatal("entered too early")
	case <-time.After(20 * time.Millisecond):
	}
}

func pollUnderBudget(check func() bool) {
	deadline := time.Now().Add(time.Second)
	// ruleid: integration-test-wall-clock-deadline
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func waitUnderTimer(t *testing.T, entered <-chan struct{}) {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	// ruleid: integration-test-wall-clock-deadline
	select {
	case <-entered:
	case <-timer.C:
		t.Fatal("did not enter")
	}
}

func proveAbsenceUnderTimer(t *testing.T, entered <-chan struct{}) {
	timer := time.NewTimer(time.Millisecond)
	defer timer.Stop()
	// ok: integration-test-wall-clock-deadline
	select {
	case <-entered:
		t.Fatal("entered too early")
	case <-timer.C:
	}
}

func waitUnderDeadlineChannel(t *testing.T, entered <-chan struct{}) {
	deadline := time.After(time.Second)
	for {
		// ruleid: integration-test-wall-clock-deadline
		select {
		case <-entered:
			return
		case <-deadline:
			t.Error("did not enter")
			return
		}
	}
}

func receiveUnderTimerWithReturn(t *testing.T, result <-chan error) error {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	// ruleid: integration-test-wall-clock-deadline
	select {
	case err := <-result:
		return err
	case <-timer.C:
		t.Error("did not receive")
		return nil
	}
}

func receiveUnderTestWithReturn(result <-chan error) error {
	// ok: integration-test-wall-clock-deadline
	return <-result
}

func proveAbsenceUnderDeadlineChannel(t *testing.T, entered <-chan struct{}) {
	deadline := time.After(time.Millisecond)
	// ok: integration-test-wall-clock-deadline
	select {
	case <-entered:
		t.Error("entered too early")
	case <-deadline:
	}
}

func waitWithError(t *testing.T, entered <-chan struct{}) {
	// ruleid: integration-test-wall-clock-deadline
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Error("did not enter")
	}
}

func proveAbsenceWithError(t *testing.T, entered <-chan struct{}) {
	// ok: integration-test-wall-clock-deadline
	select {
	case <-entered:
		t.Error("entered too early")
	case <-time.After(time.Millisecond):
	}
}

func waitWithFormattedError(t *testing.T, entered <-chan struct{}) {
	// ruleid: integration-test-wall-clock-deadline
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Errorf("did not enter in %s", time.Second)
		return
	}
}

func proveAbsenceWithFormattedError(t *testing.T, entered <-chan struct{}) {
	// ok: integration-test-wall-clock-deadline
	select {
	case <-entered:
		t.Errorf("entered within %s", time.Millisecond)
	case <-time.After(time.Millisecond):
	}
}

func waitOnSeveralEvents(t *testing.T, entered, stopped <-chan struct{}) {
	// ruleid: integration-test-wall-clock-deadline
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Error("neither event arrived")
	case <-stopped:
	}
}

func proveSeveralEventsAbsent(t *testing.T, entered, stopped <-chan struct{}) {
	// ok: integration-test-wall-clock-deadline
	select {
	case <-entered:
		t.Error("entered too early")
	case <-time.After(time.Millisecond):
	case <-stopped:
		t.Error("stopped too early")
	}
}

func pollAfterDeadline(t *testing.T, check func() bool) {
	deadline := time.Now().Add(time.Second)
	for !check() {
		// ruleid: integration-test-wall-clock-deadline
		if time.Now().After(deadline) {
			t.Errorf("did not finish in %s", time.Second)
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func pollAfterFatalDeadline(t *testing.T, check func() bool) {
	deadline := time.Now().Add(time.Second)
	for !check() {
		// ruleid: integration-test-wall-clock-deadline
		if time.Now().After(deadline) {
			t.Fatal("did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func pollAfterInjectedDeadline(t *testing.T, now func() time.Time, deadline time.Time) {
	// ok: integration-test-wall-clock-deadline
	if now().After(deadline) {
		t.Fatal("injected deadline fired")
	}
}

func waitOnTimerAndSeveralEvents(t *testing.T, first, second, third <-chan error) {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	// ruleid: integration-test-wall-clock-deadline
	select {
	case err := <-first:
		t.Log(err)
	case <-timer.C:
		t.Errorf("no event within %s", time.Second)
		return
	case err := <-second:
		t.Log(err)
	case err := <-third:
		t.Log(err)
	}
}

func proveSeveralEventsAbsentUnderTimer(t *testing.T, first, second, third <-chan error) {
	timer := time.NewTimer(time.Millisecond)
	defer timer.Stop()
	// ok: integration-test-wall-clock-deadline
	select {
	case <-first:
		t.Error("first event arrived too early")
	case <-timer.C:
	case <-second:
		t.Error("second event arrived too early")
	case <-third:
		t.Error("third event arrived too early")
	}
}

func returningDeadlineAsInput(t *testing.T) error {
	// ok: integration-test-wall-clock-deadline
	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	<-ctx.Done()
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatal(ctx.Err())
	}
	return ctx.Err()
}

func returningDeadlineWithoutExpiryAssertion(t *testing.T) error {
	// ruleid: integration-test-wall-clock-deadline
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	return ctx.Err()
}

func timestampAfterDeadline(t *testing.T) {
	deadline := time.Now().Add(time.Second)
	// ok: integration-test-wall-clock-deadline
	if time.Now().After(deadline) {
		t.Log("timestamp is after the deadline")
	}
}

func pollBeforeDeadlineWithState(check func() bool) {
	deadline := time.Now().Add(time.Second)
	// ruleid: integration-test-wall-clock-deadline
	for !check() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
}

func pollWithoutDeadline(check func() bool) {
	// ok: integration-test-wall-clock-deadline
	for !check() {
		time.Sleep(time.Millisecond)
	}
}

func waitOnInjectedTimer(t *testing.T, timer *time.Timer, entered <-chan struct{}) {
	// ok: integration-test-wall-clock-deadline
	select {
	case <-entered:
	case <-timer.C:
		t.Fatal("injected clock fired")
	}
}

func deadlineAsInput(t *testing.T) {
	// ok: integration-test-wall-clock-deadline
	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	<-ctx.Done()
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatal(ctx.Err())
	}
}

func fixedDeadlineAsInput(t *testing.T) {
	// ok: integration-test-wall-clock-deadline
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(time.Millisecond))
	defer cancel()
	<-ctx.Done()
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatal(ctx.Err())
	}
}

func deadlineInSubtest(t *testing.T) {
	t.Run("expiry", func(t *testing.T) {
		// ok: integration-test-wall-clock-deadline
		ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
		defer cancel()
		<-ctx.Done()
		if ctx.Err() != context.DeadlineExceeded {
			t.Fatal(ctx.Err())
		}
	})
}

func deadlineWithoutExpiryAssertion(t *testing.T) {
	// ruleid: integration-test-wall-clock-deadline
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_ = ctx
}

func fixedDeadlineWithoutExpiryAssertion(t *testing.T) {
	// ruleid: integration-test-wall-clock-deadline
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(time.Second))
	defer cancel()
	_ = ctx
}

func pollUnderTest(check func() bool) {
	// ok: integration-test-wall-clock-deadline
	for !check() {
		time.Sleep(time.Millisecond)
	}
}

func pollForPastStart(t *testing.T, check func(time.Time) bool) {
	// ok: integration-test-wall-clock-deadline
	start := time.Now().Add(-time.Hour)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !check(start) {
		select {
		case <-ticker.C:
		case <-t.Context().Done():
			return
		}
	}
}

func cleanupBeforeInlineClockFailure(t *testing.T, done <-chan struct{}, mu *sync.Mutex) {
	// ruleid: integration-test-wall-clock-deadline
	select {
	case <-done:
	case <-time.After(time.Second):
		mu.Unlock()
		t.Fatal("lookup did not acquire token")
	}
}

func cleanupBeforeDeadlineChannelFailure(t *testing.T, done <-chan struct{}, cleanup func()) {
	deadline := time.After(time.Second)
	// ruleid: integration-test-wall-clock-deadline
	select {
	case <-done:
	case <-deadline:
		cleanup()
		t.Fatalf("did not finish in %s", time.Second)
	}
}

func cleanupBeforeTimerFailure(t *testing.T, done <-chan struct{}, cleanup func()) {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	// ruleid: integration-test-wall-clock-deadline
	select {
	case <-done:
	case <-timer.C:
		cleanup()
		t.Error("did not finish")
		return
	}
}

func assignExpiryError(done <-chan error) error {
	var nestedErr error
	// ruleid: integration-test-wall-clock-deadline
	select {
	case nestedErr = <-done:
	case <-time.After(2 * time.Second):
		nestedErr = errors.New("nested administrative call blocked during delivery")
		return nestedErr
	}
	return nestedErr
}

func returnExpiryError(done <-chan error) error {
	// ruleid: integration-test-wall-clock-deadline
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		return errors.New("administrative call did not finish within deadline")
	}
}

func returnDeadlineChannelError(done <-chan error) error {
	deadline := time.After(time.Second)
	// ruleid: integration-test-wall-clock-deadline
	select {
	case err := <-done:
		return err
	case <-deadline:
		return fmt.Errorf("did not finish in %s", time.Second)
	}
}

func returnTimerError(done <-chan error) error {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	// ruleid: integration-test-wall-clock-deadline
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return errors.New("did not finish")
	}
}

func absenceWindowReturnsNil(done <-chan struct{}) error {
	// ok: integration-test-wall-clock-deadline
	select {
	case <-done:
		return errors.New("finished too early")
	case <-time.After(time.Millisecond):
		return nil
	}
}

func virtualClockWait(t *testing.T, done <-chan struct{}) {
	synctest.Test(t, func(tt *testing.T) {
		// ok: integration-test-wall-clock-deadline
		select {
		case <-done:
		case <-time.After(time.Second):
			tt.Fatal("did not finish")
		}
	})
	// ruleid: integration-test-wall-clock-deadline
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("did not finish")
	}
}

func virtualRequestDeadline(t *testing.T) {
	synctest.Test(t, func(tt *testing.T) {
		// ok: integration-test-wall-clock-deadline
		ctx, cancel := context.WithTimeout(tt.Context(), time.Second)
		defer cancel()
		<-ctx.Done()
	})
}

func virtualTimersAndPolling(t *testing.T, done <-chan error, check func() bool) {
	synctest.Test(t, func(tt *testing.T) {
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		// ok: integration-test-wall-clock-deadline
		select {
		case <-done:
		case <-timer.C:
			tt.Error("did not finish")
		}
		deadline := time.After(time.Second)
		// ok: integration-test-wall-clock-deadline
		select {
		case <-done:
		case <-deadline:
			tt.Error("did not finish")
		}
		until := time.Now().Add(time.Second)
		// ok: integration-test-wall-clock-deadline
		for time.Now().Before(until) {
			if check() {
				return
			}
			time.Sleep(time.Millisecond)
		}
	})
}

func deferredReceiveAfterConsumed(t *testing.T, done chan error) {
	defer func() { <-done }()
	// ruleid: test-deferred-receive-after-consumed
	select {
	case result := <-done:
		t.Fatalf("finished too early: %v", result)
	default:
	}
}

func deferredReceiveAfterBareConsumed(t *testing.T, done chan struct{}) {
	defer func() {
		if result := <-done; result != (struct{}{}) {
			t.Log(result)
		}
	}()
	// ruleid: test-deferred-receive-after-consumed
	select {
	case <-done:
		t.Error("finished too early")
	default:
	}
}

func deferredReceiveAfterPutBack(t *testing.T, done chan error) {
	defer func() { <-done }()
	// ok: test-deferred-receive-after-consumed
	select {
	case result := <-done:
		done <- result
		t.Fatalf("finished too early: %v", result)
	default:
	}
}

func deferredReceiveOnOtherChannel(t *testing.T, done, other chan error) {
	defer func() { <-other }()
	// ok: test-deferred-receive-after-consumed
	select {
	case result := <-done:
		t.Fatalf("finished too early: %v", result)
	default:
	}
}

func deferredReceiveAfterLatePutBack(t *testing.T, done chan error) {
	defer func() { <-done }()
	// ruleid: test-deferred-receive-after-consumed
	select {
	case result := <-done:
		t.Fatalf("finished too early: %v", result)
		done <- result
	default:
	}
}

func deferredReceiveAfterGoroutineClose(t *testing.T) {
	closed := make(chan struct{})
	defer func() { <-closed }()
	go func() {
		close(closed)
	}()
	// ok: test-deferred-receive-after-consumed
	select {
	case <-closed:
		t.Fatal("connection closed too early")
	default:
	}
}

func cleanupReceiveAfterConsumed(t *testing.T, serverErr chan error) {
	t.Cleanup(func() {
		if err := <-serverErr; err != nil {
			t.Log(err)
		}
	})
	// ruleid: test-deferred-receive-after-consumed
	select {
	case err := <-serverErr:
		t.Fatalf("server finished too early: %v", err)
	default:
	}
}

func cleanupReceiveAfterPutBack(t *testing.T, serverErr chan error) {
	t.Cleanup(func() {
		if err := <-serverErr; err != nil {
			t.Log(err)
		}
	})
	// ok: test-deferred-receive-after-consumed
	select {
	case err := <-serverErr:
		t.Log("server returned")
		serverErr <- err
		t.Fatalf("server finished too early: %v", err)
	default:
	}
}

func deferredReceiveAfterLateNonfatalPutBack(t *testing.T, done chan error) {
	defer func() { <-done }()
	// ruleid: test-deferred-receive-after-consumed
	select {
	case err := <-done:
		t.Error("finished too early")
		done <- err
	default:
	}
}

func deferredReceiveWithFailureInMiddle(t *testing.T, done chan error) {
	defer func() { <-done }()
	// ruleid: test-deferred-receive-after-consumed
	select {
	case err := <-done:
		t.Log(err)
		t.Error("finished too early")
		t.Log("value consumed")
	default:
	}
}

func deferredReceiveAfterBarePutBack(t *testing.T, done chan struct{}) {
	defer func() { <-done }()
	// ok: test-deferred-receive-after-consumed
	select {
	case <-done:
		t.Log("finished")
		done <- struct{}{}
		t.Error("finished too early")
		t.Log("value restored")
	default:
	}
}

func deferredReceiveWithHelperReturn(done chan error, failed func(error) error) error {
	defer func() { <-done }()
	// ok: test-deferred-receive-after-consumed
	select {
	case err := <-done:
		return failed(err)
	default:
		return nil
	}
}

func deferredReceiveWithFlag(done chan error) bool {
	defer func() { <-done }()
	finished := false
	// ok: test-deferred-receive-after-consumed
	select {
	case <-done:
		finished = true
	default:
	}
	return finished
}
