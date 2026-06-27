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

package mcp

import (
	"context"
	"errors"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestServerToolSnapshot(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	server, err := NewServer(&fakeSource{}, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Run(ctx, serverTransport)
	}()

	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() {
		_ = session.Close()
		cancel()
		if err := <-serverErr; err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("server run: %v", err)
		}
	}()

	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	want := []struct {
		name        string
		description string
	}{
		{
			name: "health",
			description: "Report Pit Officer deployment health: per-node " +
				"engine version, build profile and liveness, and store reachability, " +
				"schema version and path. Read-only status only - exposes no secrets " +
				"and no order-flow control.",
		},
		{
			name: "get_account_state",
			description: "Return account row and its account-scoped risk barriers. " +
				"Read-only - no secrets, no order-flow control.",
		},
		{
			name: "get_limits",
			description: "Return risk barriers, optionally filtered by account. " +
				"Read-only - no secrets, no order-flow control.",
		},
		{
			name: "get_order",
			description: "Return one order addressed by its external id: its " +
				"status, its 1:1 signed approval (when issued), and its fills with " +
				"their display prices. Read-only - no secrets, no order-flow control. " +
				"The opaque reservation lock is never exposed; only backend-derived " +
				"display prices are.",
		},
		{
			name: "get_audit",
			description: "Return recent control-plane audit entries (default 50, " +
				"max 500). Read-only - no secrets, no order-flow control.",
		},
		{
			name: "check_order",
			description: "Run a non-mutating pre-trade dry-run for an order: report " +
				"whether it would pass the engine's risk checks plus the reasons " +
				"(would-be lock prices on pass, structured rejects and any would-be " +
				"account block on reject). Submits nothing and changes no state - no " +
				"order is placed, no funds are reserved.",
		},
		{
			name: "set_market_data_instrument",
			description: "Enable or disable one configured market-data instrument. " +
				"Mutates Officer control-plane config only; protected and disabled " +
				"by default.",
		},
		{
			name: "submit_order",
			description: "Submit an order intent through pre-trade and obtain a " +
				"signed approval token. Mutates engine state (holds or commits " +
				"funds); protected and disabled by default.",
		},
		{
			name: "confirm_execution",
			description: "Confirm execution (commit) of a previously approved " +
				"hold-mode order, addressed by its external id, by presenting the " +
				"approval token. Mutates engine state; protected and disabled by " +
				"default.",
		},
		{
			name: "cancel",
			description: "Cancel / revoke a pending approval token or held " +
				"reservation by presenting the token. Releases held funds. Mutates " +
				"engine state; protected and disabled by default.",
		},
	}
	if len(list.Tools) != len(want) {
		t.Fatalf("tool count: want %d got %d", len(want), len(list.Tools))
	}
	gotByName := make(map[string]*sdkmcp.Tool, len(list.Tools))
	for _, got := range list.Tools {
		gotByName[got.Name] = got
	}
	for _, w := range want {
		got, ok := gotByName[w.name]
		if !ok {
			t.Fatalf("missing tool %q in tools/list: %+v", w.name, list.Tools)
		}
		if got.Description != w.description {
			t.Fatalf("tool %s description mismatch\nwant: %q\ngot:  %q",
				w.name, w.description, got.Description)
		}
		if got.InputSchema == nil || got.OutputSchema == nil {
			t.Fatalf("tool %s must advertise input and output schemas", w.name)
		}
	}
}
