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

package trading

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestIBManagedAccountsTrailingComma(t *testing.T) {
	v := newFakeIBVenue()
	v.accounts = strings.Split(" DU123, ", ",")
	c := testIBConnector(t, v)
	info, err := c.CheckAccount(context.Background())
	if err != nil || info.Identity != "DU123" || info.Status != "active" {
		t.Fatalf("accounts=%+v err=%v", info, err)
	}
}

func TestIBProbeSerialization(t *testing.T) {
	v := newFakeIBVenue()
	firstEntered := make(chan struct{})
	release := make(chan struct{})
	v.connectHook = func(client *fakeIBTradingClient) {
		v.mu.Lock()
		first := v.connects == 1
		v.mu.Unlock()
		if first {
			close(firstEntered)
			<-release
		}
	}
	c1, c2 := testIBConnector(t, v), testIBConnector(t, v)
	results := make(chan error, 2)
	go func() { _, err := c1.CheckAccount(context.Background()); results <- err }()
	<-firstEntered
	secondStarted := make(chan struct{})
	go func() { close(secondStarted); _, err := c2.CheckAccount(context.Background()); results <- err }()
	<-secondStarted
	// Keep the first Connect outstanding while the second probe tries the
	// same process-wide clientId lock. An overlap is measured by the fake.
	time.Sleep(30 * time.Millisecond)
	v.mu.Lock()
	maxActive := v.maxActive
	v.mu.Unlock()
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if maxActive != 1 || v.maxActive != 1 {
		t.Fatalf("probe sessions overlap: max active=%d", v.maxActive)
	}
	if v.connects != 2 || v.disconnects != 2 {
		t.Fatalf("probe cleanup connects=%d disconnects=%d", v.connects, v.disconnects)
	}
	ibProbeLocks.Lock()
	remaining := len(ibProbeLocks.entries)
	ibProbeLocks.Unlock()
	if remaining != 0 {
		t.Fatal("probe lock map retained idle entries")
	}
}

func TestIBProbeCleanup(t *testing.T) {
	for _, kind := range []string{"success", "error", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			v := newFakeIBVenue()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "error" {
				v.connectError = errors.New("fake connect refused")
			}
			if kind == "cancel" {
				v.connectHook = func(*fakeIBTradingClient) { cancel() }
			}
			c := testIBConnector(t, v)
			_, err := c.CheckAccount(ctx)
			if kind == "success" && err != nil || kind != "success" && err == nil {
				t.Fatalf("probe err=%v", err)
			}
			v.mu.Lock()
			connects, disconnects := v.connects, v.disconnects
			v.mu.Unlock()
			if connects != 1 || disconnects != 1 {
				t.Fatalf("cleanup connects=%d disconnects=%d", connects, disconnects)
			}
		})
	}
}
