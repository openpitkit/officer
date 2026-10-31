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

package ib_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/scmhub/ibapi"

	"go.openpit.dev/officer/internal/ib"
)

func TestForceDisconnect(t *testing.T) {
	if err := ib.ForceDisconnect(nil); err != nil {
		t.Fatalf("ForceDisconnect(nil) = %v, want nil", err)
	}
	if err := ib.ForceDisconnect(ibapi.NewEClient(nil)); err != nil {
		t.Fatalf("ForceDisconnect(new client) = %v, want nil", err)
	}
}

func TestClientID(t *testing.T) {
	for _, id := range []string{"", " \t\n"} {
		if got := ib.ClientID(id); got != ib.DefaultFallbackClientID {
			t.Fatalf("ClientID(%q) = %d, want fallback %d", id, got, ib.DefaultFallbackClientID)
		}
	}
	if ib.DefaultFallbackClientID != 1000 {
		t.Fatalf("fallback = %d, want 1000", ib.DefaultFallbackClientID)
	}
	// The published FNV-32a test vector for "hello" is 0x4f9f2cab.
	const wantHello = 1001 + int64(uint32(0x4f9f2cab)%899999)
	if got := ib.ClientID(" \thello\n"); got != wantHello {
		t.Fatalf("trimmed FNV-32a ClientID = %d, want %d", got, wantHello)
	}
	for i := 0; i < 10000; i++ {
		id := fmt.Sprintf("ib-instance-%d", i)
		got := ib.ClientID(id)
		if got < 1001 || got > 900999 {
			t.Fatalf("ClientID(%q) = %d, want 1001..900999", id, got)
		}
		if again := ib.ClientID(id); got != again {
			t.Fatalf("ClientID(%q) changed from %d to %d", id, got, again)
		}
	}
}

func TestClassifiers(t *testing.T) {
	fatal := map[int64]bool{502: true, 504: true, 1300: true}
	informational := map[int64]bool{
		1102: true, 2104: true, 2106: true, 2107: true, 2108: true, 2158: true,
	}
	recoverable := map[int64]bool{
		1100: true, 1101: true, 2103: true, 2105: true, 2110: true,
	}
	for code := int64(-1); code <= 3000; code++ {
		if got := ib.FatalError(code); got != fatal[code] {
			t.Errorf("FatalError(%d) = %v, want %v", code, got, fatal[code])
		}
		if got := ib.InformationalError(code); got != informational[code] {
			t.Errorf("InformationalError(%d) = %v, want %v", code, got, informational[code])
		}
		if got := ib.RecoverableConnectionError(code); got != recoverable[code] {
			t.Errorf("RecoverableConnectionError(%d) = %v, want %v", code, got, recoverable[code])
		}
	}
}

func TestConnectTimeout(t *testing.T) {
	disconnectErr := errors.New("forced disconnect failed")
	client := &timeoutClient{
		release:       make(chan struct{}),
		finished:      make(chan struct{}),
		disconnectErr: disconnectErr,
	}
	// Also release Connect on an assertion failure so the test leaves no worker.
	defer client.releaseConnect()
	err := ib.Connect(context.Background(), client, "tws.test", 4002, 71, 10*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Connect error = %v, want context deadline exceeded", err)
	}
	if !errors.Is(err, disconnectErr) {
		t.Fatalf("Connect error = %v, want forced disconnect error", err)
	}
	if client.forced != 1 || client.disconnected != 0 {
		t.Fatalf("disconnect calls: force = %d, plain = %d; want 1, 0",
			client.forced, client.disconnected)
	}
	select {
	case <-client.finished:
	default:
		t.Fatal("Connect returned without draining the connect result")
	}
	if client.host != "tws.test" || client.port != 4002 || client.clientID != 71 {
		t.Fatalf("Connect arguments = %q, %d, %d; want tws.test, 4002, 71",
			client.host, client.port, client.clientID)
	}
}

type timeoutClient struct {
	release       chan struct{}
	finished      chan struct{}
	disconnectErr error
	host          string
	port          int
	clientID      int64
	forced        int
	disconnected  int
}

func (c *timeoutClient) Connect(host string, port int, clientID int64) error {
	c.host, c.port, c.clientID = host, port, clientID
	<-c.release
	close(c.finished)
	return nil
}

func (c *timeoutClient) Disconnect() error {
	c.disconnected++
	c.releaseConnect()
	return nil
}

func (c *timeoutClient) ForceDisconnect() error {
	c.forced++
	c.releaseConnect()
	return c.disconnectErr
}

func (c *timeoutClient) releaseConnect() {
	select {
	case <-c.release:
	default:
		close(c.release)
	}
}
