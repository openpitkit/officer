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

package node

import (
	"errors"
	"runtime"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
)

const identityGateTestTimeout = time.Second

type identityGateSink struct{}

func (*identityGateSink) Push(marketdata.QuoteUpdate) error { return nil }

type identityGateEngine struct {
	*fakeEngine
	sink marketdata.Sink
}

func (e *identityGateEngine) MarketDataSink() marketdata.Sink { return e.sink }

func TestLocalNode_LiveIdentityPublicationWaitsForActiveLane(t *testing.T) {
	t.Parallel()
	n, _ := newTestNode(t, newFakeEngine())

	_, endLane, err := n.beginLane()
	if err != nil {
		t.Fatalf("beginLane: %v", err)
	}
	laneEnded := false
	defer func() {
		if !laneEnded {
			endLane()
		}
	}()

	started := make(chan struct{})
	acquired := make(chan struct{})
	release := make(chan struct{})
	errs := make(chan error, 1)
	go func() {
		close(started)
		err := n.beginLiveIdentityPublication()
		if err == nil {
			close(acquired)
			<-release
			n.endLiveIdentityPublication()
		}
		errs <- err
	}()
	<-started

	assertIdentityGateBlocked(t, acquired)
	endLane()
	laneEnded = true
	waitIdentityGateSignal(t, acquired, "live identity publication did not acquire")
	close(release)
	if err := <-errs; err != nil {
		t.Fatalf("beginLiveIdentityPublication: %v", err)
	}
}

func TestLocalNode_LiveIdentityPublicationFencesNewLanesWithoutRestartOrSwap(
	t *testing.T,
) {
	t.Parallel()
	base := newFakeEngine()
	n, _ := newTestNode(t, base)
	sink := &identityGateSink{}
	eng := &identityGateEngine{fakeEngine: base, sink: sink}
	n.engineMu.Lock()
	n.engine = eng
	n.engineMu.Unlock()
	buildCalls := 0
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		buildCalls++
		return newFakeEngine(), nil
	}

	if err := n.beginLiveIdentityPublication(); err != nil {
		t.Fatalf("beginLiveIdentityPublication: %v", err)
	}
	released := false
	defer func() {
		if !released {
			n.endLiveIdentityPublication()
		}
	}()

	type laneResult struct {
		engine engine.Engine
		end    func()
		err    error
	}
	laneStarted := make(chan struct{})
	laneResults := make(chan laneResult, 1)
	go func() {
		close(laneStarted)
		got, end, err := n.beginLane()
		laneResults <- laneResult{engine: got, end: end, err: err}
	}()
	<-laneStarted
	assertIdentityGateBlocked(t, laneResults)

	if n.restarting.Load() {
		t.Fatal("live identity publication set engine restart semantics")
	}
	if got := n.currentEngine(); got != eng {
		t.Fatalf("engine changed while gate held: got %T, want %T", got, eng)
	}
	if got := n.currentMarketDataSink(); got != sink {
		t.Fatalf("market-data sink changed while gate held: got %T, want %T", got, sink)
	}

	n.endLiveIdentityPublication()
	released = true
	var lane laneResult
	select {
	case lane = <-laneResults:
	case <-time.After(identityGateTestTimeout):
		t.Fatal("account lane did not enter after live identity publication ended")
	}
	if lane.err != nil {
		t.Fatalf("beginLane after publication: %v", lane.err)
	}
	if lane.engine != eng {
		t.Fatalf("lane engine = %T, want unchanged %T", lane.engine, eng)
	}
	lane.end()

	if buildCalls != 0 {
		t.Fatalf("live identity publication build calls = %d, want 0", buildCalls)
	}
	if got := n.currentEngine(); got != eng {
		t.Fatalf("engine after release = %T, want %T", got, eng)
	}
	if got := n.currentMarketDataSink(); got != sink {
		t.Fatalf("market-data sink after release = %T, want %T", got, sink)
	}
}

func TestLocalNode_LiveIdentityPublicationRejectsEngineRestart(t *testing.T) {
	t.Parallel()
	n, _ := newTestNode(t, newFakeEngine())
	n.restarting.Store(true)

	err := n.beginLiveIdentityPublication()
	if !errors.Is(err, domain.ErrEngineRestarting) {
		t.Fatalf("beginLiveIdentityPublication error = %v, want ErrEngineRestarting", err)
	}

	n.restarting.Store(false)
	if !n.laneGate.TryLock() {
		t.Fatal("failed live identity publication left lane admission fenced")
	}
	n.laneGate.Unlock()
}

func TestLocalNode_LiveIdentityPublicationRechecksRestartAfterMutationWait(
	t *testing.T,
) {
	t.Parallel()
	n, _ := newTestNode(t, newFakeEngine())
	n.mutate.Lock()
	mutationLocked := true
	defer func() {
		if mutationLocked {
			n.mutate.Unlock()
		}
	}()

	errs := make(chan error, 1)
	go func() { errs <- n.beginLiveIdentityPublication() }()
	waitIdentityGateWriter(t, &n.laneGate)
	n.restarting.Store(true)
	n.mutate.Unlock()
	mutationLocked = false

	var err error
	select {
	case err = <-errs:
	case <-time.After(identityGateTestTimeout):
		t.Fatal("live identity publication did not finish after mutation lock release")
	}
	if !errors.Is(err, domain.ErrEngineRestarting) {
		t.Fatalf("beginLiveIdentityPublication error = %v, want ErrEngineRestarting", err)
	}
	n.restarting.Store(false)
	if !n.laneGate.TryLock() {
		t.Fatal("restart rejection left lane admission fenced")
	}
	n.laneGate.Unlock()
	if !n.mutate.TryLock() {
		t.Fatal("restart rejection left mutation serialization locked")
	}
	n.mutate.Unlock()
}

func TestLocalNode_EngineRestartFromLaneReservesBeforeRelease(t *testing.T) {
	t.Parallel()
	n, _ := newTestNode(t, newFakeEngine())
	endLaneEntered := make(chan struct{})
	endLaneRelease := make(chan struct{})
	restartDone := make(chan error, 1)

	n.laneGate.RLock()
	go func() {
		restartDone <- n.beginEngineRestartFromLane(func() {
			close(endLaneEntered)
			<-endLaneRelease
			n.laneGate.RUnlock()
		})
	}()

	waitIdentityGateSignal(t, endLaneEntered, "lane release hook did not start")
	if !n.restarting.Load() {
		t.Fatal("restart was not reserved before the admitted lane released")
	}
	if err := n.beginMutation(); !errors.Is(err, domain.ErrEngineRestarting) {
		t.Fatalf("beginMutation error = %v, want engine restarting", err)
	}
	if _, done, err := n.beginLane(); !errors.Is(err, domain.ErrEngineRestarting) {
		if done != nil {
			done()
		}
		t.Fatalf("beginLane error = %v, want engine restarting", err)
	}

	close(endLaneRelease)
	if err := <-restartDone; err != nil {
		t.Fatalf("beginEngineRestartFromLane: %v", err)
	}
	n.endEngineRestart()
}

func assertIdentityGateBlocked[Value any](t *testing.T, ch <-chan Value) {
	t.Helper()
	select {
	case <-ch:
		t.Fatal("operation entered while the exclusive gate should block it")
	case <-time.After(50 * time.Millisecond):
	}
}

func waitIdentityGateSignal(t *testing.T, ch <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(identityGateTestTimeout):
		t.Fatal(failure)
	}
}

func waitIdentityGateWriter(t *testing.T, gate interface {
	TryRLock() bool
	RUnlock()
}) {
	t.Helper()
	deadline := time.Now().Add(identityGateTestTimeout)
	for time.Now().Before(deadline) {
		if !gate.TryRLock() {
			return
		}
		gate.RUnlock()
		runtime.Gosched()
	}
	t.Fatal("live identity publication did not fence lane admission")
}
