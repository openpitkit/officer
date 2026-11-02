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
	"testing"
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
