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
	"database/sql"
	"sync"
	"testing"
	"time"
)

func resetBeforeConnection(t *testing.T, db *sql.DB, reset func() error) {
	result := make(chan error, 1)
	count := db.Stats().WaitCount
	go func() { result <- reset() }()
	// ruleid: goroutine-signal-without-result
	for db.Stats().WaitCount == count {
		time.Sleep(time.Millisecond)
	}
	_ = <-result
}

func publishBeforeDelivery(publish func() error, started chan struct{}) {
	published := make(chan error, 1)
	go func() { published <- publish() }()
	// ruleid: goroutine-signal-without-result
	<-started
	_ = <-published
}

func tickBeforeCallback(tick func() error, entered chan struct{}) {
	done := make(chan error, 1)
	go func() { _, err := 0, tick(); done <- err }()
	// ruleid: goroutine-signal-without-result
	<-entered
	_ = <-done
}

func assignedSignal(run func() error, signal chan int) {
	result := make(chan error, 1)
	go func() { result <- run() }()
	// ruleid: goroutine-signal-without-result
	x := <-signal
	// ruleid: goroutine-signal-without-result
	x = <-signal
	_ = x
	_ = <-result
}

func waitWithResult(t *testing.T, run func() error, signal chan struct{}) {
	result := make(chan error, 1)
	go func() { result <- run() }()
	// ok: goroutine-signal-without-result
	select {
	case <-signal:
	case err := <-result:
		t.Fatalf("returned before signal: %v", err)
	}
	// ok: goroutine-signal-without-result
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func waitForOwnResult(run func() error) {
	result := make(chan error, 1)
	go func() { result <- run() }()
	// ok: goroutine-signal-without-result
	err := <-result
	_ = err
}

func noResultChannel(run func(), signal chan struct{}) {
	go func() { run() }()
	// ok: goroutine-signal-without-result
	<-signal
}

func assignedSelect(t *testing.T, run func() error, signal chan int) {
	result := make(chan error, 1)
	go func() { result <- run() }()
	var err error
	// ok: goroutine-signal-without-result
	select {
	case x := <-signal:
		_ = x
	case err = <-result:
		t.Fatalf("returned before signal: %v", err)
	}
	_ = <-result
}

func pollWithResult(t *testing.T, db *sql.DB, reset func() error) {
	result := make(chan error, 1)
	count := db.Stats().WaitCount
	go func() { result <- reset() }()
	// ok: goroutine-signal-without-result
	for db.Stats().WaitCount == count {
		select {
		case err := <-result:
			t.Fatalf("returned before connection wait: %v", err)
		default:
		}
		time.Sleep(time.Millisecond)
	}
	_ = <-result
}

func checkSignalAfterResult(t *testing.T, run func() error, signal chan struct{}) {
	result := make(chan error, 1)
	go func() { result <- run() }()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	// ok: goroutine-signal-without-result
	select {
	case <-signal:
	default:
		t.Fatal("returned without signal")
	}
}

func waitInSelectWithoutResult(run func() error, signal chan struct{}) {
	result := make(chan error, 1)
	go func() { result <- run() }()
	// ruleid: goroutine-signal-without-result
	select {
	case <-signal:
	}
	_ = <-result
}

func observeAbsence(t *testing.T, run func() error, signal chan struct{}) {
	result := make(chan error, 1)
	go func() { result <- run() }()
	// ok: goroutine-signal-without-result
	select {
	case <-signal:
		t.Fatal("signal arrived too early")
	case <-time.After(20 * time.Millisecond):
	}
	_ = <-result
}

func separateResults(t *testing.T, run func() error, start chan struct{}) {
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { first <- run() }()
	// ok: goroutine-signal-without-result
	go func() {
		<-start
		second <- run()
	}()
	close(start)
	// ok: goroutine-signal-without-result
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	// ok: goroutine-signal-without-result
	if err := <-second; err != nil {
		t.Fatal(err)
	}
}

func commaOKSignal(run func() error, signal chan int) {
	result := make(chan error, 1)
	go func() { result <- run() }()
	// ruleid: goroutine-signal-without-result
	v, ok := <-signal
	// ruleid: goroutine-signal-without-result
	v, ok = <-signal
	_, _ = v, ok
	_ = <-result
}

func rangeSignal(run func() error, signal chan int) {
	result := make(chan error, 1)
	go func() { result <- run() }()
	// ruleid: goroutine-signal-without-result
	for range signal {
	}
	// ruleid: goroutine-signal-without-result
	for v := range signal {
		_ = v
	}
	_ = <-result
}

func localRangeSignal(run func() error) {
	result := make(chan error, 1)
	signal := make(chan int)
	go func() { result <- run() }()
	// ruleid: goroutine-signal-without-result
	for range signal {
	}
	// ruleid: goroutine-signal-without-result
	for v := range signal {
		_ = v
	}
	_ = <-result
}

func collectLocalChannel(run func() error) {
	result := make(chan error, 1)
	b := make(chan int, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		result <- run()
		// todook: goroutine-signal-without-result
		b <- 1
	}()
	wg.Wait()
	// todook: goroutine-signal-without-result
	close(b)
	// todook: goroutine-signal-without-result
	for v := range b {
		_ = v
	}
	// todook: goroutine-signal-without-result
	_ = <-result
}

func rangeOwnResult(run func() error) {
	result := make(chan error, 1)
	go func() { result <- run() }()
	// ok: goroutine-signal-without-result
	for range result {
	}
	// ok: goroutine-signal-without-result
	for err := range result {
		_ = err
	}
}

func nonChannelRange(run func() error, values []int, count int) {
	result := make(chan error, 1)
	go func() { result <- run() }()
	// ok: goroutine-signal-without-result
	for range 3 {
	}
	// ok: goroutine-signal-without-result
	for range count {
	}
	// ok: goroutine-signal-without-result
	for v := range values {
		_ = v
	}
	_ = <-result
}

func receiveOnlyRange(run func() error, signal <-chan int) {
	result := make(chan error, 1)
	go func() { result <- run() }()
	// ruleid: goroutine-signal-without-result
	for range signal {
	}
	_ = <-result
}

func rangeField(run func() error, state struct{ signal chan int }) {
	result := make(chan error, 1)
	go func() { result <- run() }()
	// todoruleid: goroutine-signal-without-result
	for range state.signal {
	}
	// todoruleid: goroutine-signal-without-result
	for v := range state.signal {
		_ = v
	}
	_ = <-result
}

func commaOKSelect(t *testing.T, run func() error, signal chan int) {
	result := make(chan error, 1)
	go func() { result <- run() }()
	// ok: goroutine-signal-without-result
	select {
	case v, ok := <-signal:
		_, _ = v, ok
	case err, ok := <-result:
		t.Fatalf("returned before signal: %v %v", err, ok)
	}
	var err error
	var ok bool
	// ok: goroutine-signal-without-result
	select {
	case <-signal:
	case err, ok = <-result:
		t.Fatalf("returned before signal: %v %v", err, ok)
	}
	// ok: goroutine-signal-without-result
	select {
	case _, ok := <-signal:
		_ = ok
	default:
	}
	_ = <-result
}

func resultAlias(run func() error, signal chan int) {
	result := make(chan error, 1)
	go func(out chan<- error) { out <- run() }(result)
	// todoruleid: goroutine-signal-without-result
	<-signal
	_ = <-result
}

func signalInInitializer(run func() error, signal chan bool) {
	result := make(chan error, 1)
	go func() { result <- run() }()
	// todoruleid: goroutine-signal-without-result
	if v := <-signal; v {
		_ = v
	}
	_ = <-result
}

func helperWait(run func() error, wait func()) {
	result := make(chan error, 1)
	go func() { result <- run() }()
	// todoruleid: goroutine-signal-without-result
	wait()
	_ = <-result
}

func blockingSignalAfterResult(run func() error, signal chan int) {
	result := make(chan error, 1)
	go func() { result <- run() }()
	err := <-result
	_ = err
	// todook: goroutine-signal-without-result
	<-signal
}
