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

package openapp

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	frameworkapp "go.openpit.dev/officer/framework/app"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	"go.openpit.dev/officer/internal/mcp/tools"
)

func TestRegisterBuildUsesPopulatedMCPCatalog(t *testing.T) {
	ctx := context.Background()
	builder := frameworkapp.NewBuilder()
	Register(builder)
	builder.SetEngineBuildFactory(func(frameworkapp.Config) engine.BuildFunc {
		return func(engine.Snapshot) (engine.Engine, error) {
			return &fakeEngine{running: true, sink: &fakeSink{}}, nil
		}
	})

	app, err := builder.Build(
		ctx,
		frameworkapp.Config{SQLitePath: filepath.Join(t.TempDir(), "officer.db")},
		slog.Default(),
		nil,
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	commands, err := app.Service().ListMcpAccess(ctx)
	if err != nil {
		t.Fatalf("ListMcpAccess: %v", err)
	}
	expected := frameworkmcp.NewToolRegistry()
	tools.RegisterTools(expected, nil)
	if len(commands) != len(expected.Descriptors()) {
		t.Fatalf("ListMcpAccess returned %d commands, want %d", len(commands), len(expected.Descriptors()))
	}

	getAccount, ok := findCommand(commands, "get_account_state")
	if !ok {
		t.Fatalf("get_account_state missing from composed catalogue")
	}
	if !getAccount.Command.DefaultEnabled || !getAccount.Enabled {
		t.Fatalf("get_account_state default state = %+v", getAccount)
	}
	if err := app.Service().SetMcpAccess(ctx, "get_account_state", false); err != nil {
		t.Fatalf("SetMcpAccess(get_account_state): %v", err)
	}
}

func findCommand(commands []backend.McpCommand, name string) (backend.McpCommand, bool) {
	for _, cmd := range commands {
		if cmd.Command.Name == name {
			return cmd, true
		}
	}
	return backend.McpCommand{}, false
}

type fakeSink struct{}

func (s *fakeSink) Push(marketdata.QuoteUpdate) error { return nil }

type fakeEngine struct {
	sink    marketdata.Sink
	running bool
}

func (e *fakeEngine) Version() string      { return "fake" }
func (e *fakeEngine) BuildProfile() string { return "test" }
func (e *fakeEngine) Running() bool        { return e.running }

func (e *fakeEngine) ConfigurePolicy(
	context.Context,
	string,
	engine.LimitSet,
) error {
	return nil
}

func (e *fakeEngine) BlockAccount(context.Context, domain.AccountID, string) error {
	return nil
}

func (e *fakeEngine) UnblockAccount(context.Context, domain.AccountID) error {
	return nil
}

func (e *fakeEngine) ApplyAccountAdjustmentBatch(
	context.Context,
	domain.AccountID,
	[]domain.AdjustmentRequest,
) ([]engine.AdjustmentResult, *engine.AdjustmentBatchReject, error) {
	return nil, nil, nil
}

func (e *fakeEngine) ApplyAccountAdjustment(
	context.Context,
	domain.AccountID,
	domain.AdjustmentRequest,
) (engine.AdjustmentResult, error) {
	return engine.AdjustmentResult{}, nil
}

func (e *fakeEngine) SubmitOrder(context.Context, domain.Order) (engine.OrderResult, error) {
	return engine.OrderResult{}, nil
}

func (e *fakeEngine) ReserveHold(context.Context, domain.Order) (engine.HoldResult, error) {
	return engine.HoldResult{}, nil
}

func (e *fakeEngine) CommitHeld(context.Context, string) error { return nil }

func (e *fakeEngine) RollbackHeld(context.Context, string) error { return nil }

func (e *fakeEngine) SubmitImmediate(
	context.Context,
	domain.Order,
) (engine.ImmediateResult, error) {
	return engine.ImmediateResult{}, nil
}

func (e *fakeEngine) SetReservationStore(engine.ReservationStore) {}

func (e *fakeEngine) ReconcileOrphans(context.Context) (int, error) { return 0, nil }

func (e *fakeEngine) ApplyExecutionReport(
	context.Context,
	domain.ExecutionReportInput,
) (engine.ExecutionReportResult, error) {
	return engine.ExecutionReportResult{}, nil
}

func (e *fakeEngine) RegisterGroup(context.Context, []domain.AccountID, string) error {
	return nil
}

func (e *fakeEngine) UnregisterGroup(context.Context, []domain.AccountID, string) error {
	return nil
}

func (e *fakeEngine) BlockGroup(context.Context, string, string) error { return nil }

func (e *fakeEngine) UnblockGroup(context.Context, string) error { return nil }

func (e *fakeEngine) CheckOrder(
	context.Context,
	domain.OrderProbe,
) (domain.CheckResult, error) {
	return domain.CheckResult{}, nil
}

func (e *fakeEngine) MarketDataSink() marketdata.Sink { return e.sink }

func (e *fakeEngine) Stop() { e.running = false }
