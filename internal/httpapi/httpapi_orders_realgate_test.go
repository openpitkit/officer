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

package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
	appstore "go.openpit.dev/officer/internal/store"
)

// TestApplyExecutionReport_RealTerminalGate drives the execution-report endpoint
// through the REAL http handler backed by a REAL backend.Service and a REAL
// localNode (SQLite store + engine fake), NOT a fakeService injecting
// domain.ErrTerminalOrder. It proves the terminal-order guard is enforced by the
// node itself: a report against an already-terminal (filled) order returns HTTP
// 409 terminal_order, and the same report with force=true bypasses the guard and
// returns 201.
func TestApplyExecutionReport_RealTerminalGate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	handler, realm := newRealServiceRouter(t)

	// Seed the order in the store already in a terminal (filled) status. The gate
	// lives in node.ApplyExecutionReport -> RequireOrderModifiable, which reads the
	// authoritative persisted status; the fake engine is never consulted for the
	// rejected case.
	const acct domain.AccountID = "acc-1"
	seedRealAccountAndAssets(t, realm, acct)
	order, err := realm.CreateOrder(ctx, domain.Order{
		Account:     acct,
		Source:      domain.SourceAPI,
		Principal:   domain.PrincipalOperator,
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "2",
		Price:       "400",
		Status:      domain.OrderStatusFilled,
	})
	if err != nil {
		t.Fatalf("CreateOrder (terminal): %v", err)
	}

	path := "/api/v1/orders/" + order.ExternalID.String() + "/execution-reports"

	// Without force: the real node gate rejects the terminal order -> 409.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path,
		bytes.NewBufferString(
			`{"quantity":"1","price":"400","leavesQuantity":"0","status":"filled"}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("terminal report without force: status = %d, want 409; body=%s",
			rec.Code, rec.Body.String())
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "terminal_order" {
		t.Fatalf("error code = %v, want terminal_order; body=%s",
			errObj["code"], rec.Body.String())
	}
	if errObj["message"] != domain.ErrTerminalOrder.Error() {
		t.Fatalf("error message = %v, want %q", errObj["message"], domain.ErrTerminalOrder.Error())
	}

	// With force: the guard is bypassed and the report is applied -> 201.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path,
		bytes.NewBufferString(
			`{"quantity":"1","price":"400","leavesQuantity":"0","status":"filled","force":true}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("terminal report with force: status = %d, want 201; body=%s",
			rec.Code, rec.Body.String())
	}
}

// newRealServiceRouter builds the full production chain a handler test would
// otherwise stub: a real SQLite store, a real localNode over an engine fake, a
// real NodeRouter, a real backend.Service, and the real HTTP router. It returns
// the mounted handler and the bound realm store for direct seeding. A nil signer
// is intentional: the execution-report path needs none, and its best-effort
// attestation tolerates the absence.
func newRealServiceRouter(t *testing.T) (http.Handler, store.RealmStore) {
	t.Helper()
	ctx := context.Background()
	st, err := appstore.NewSQLiteStore(t.TempDir() + "/httpapi-realgate.db")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	realm, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	eng := &realGateEngine{running: true}
	n, _, err := node.NewLocalNode(ctx, st, func(engine.Snapshot) (engine.Engine, error) {
		return eng, nil
	})
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	router, err := node.NewLocalRouter(n)
	if err != nil {
		t.Fatalf("NewLocalRouter: %v", err)
	}
	svc := backend.New(router, nil, nil)
	handler, err := newRouter(svc)
	if err != nil {
		t.Fatalf("newRouter: %v", err)
	}
	return handler, realm
}

// seedRealAccountAndAssets registers the account and instrument assets the order
// FK requires. The operator principal is seeded by NewLocalNode.
func seedRealAccountAndAssets(t *testing.T, realm store.RealmStore, id domain.AccountID) {
	t.Helper()
	ctx := context.Background()
	if _, err := realm.CreateAccount(ctx, domain.Account{Code: id}); err != nil {
		t.Fatalf("CreateAccount(%s): %v", id, err)
	}
	for _, code := range []string{"AAPL", "USD"} {
		if err := realm.CreateAsset(ctx, domain.Asset{Code: code}); err != nil {
			t.Fatalf("CreateAsset(%s): %v", code, err)
		}
	}
}

// realGateEngine is a minimal engine.Engine fake sufficient for the
// execution-report terminal-gate test: it runs the account lane inline and, on
// the force path, returns a valid single-fill persistence write-set so the node
// completes the settlement. It carries no risk logic; the gate under test is the
// node's own terminal-order guard, not the engine.
type realGateEngine struct {
	running bool
}

func (e *realGateEngine) Version() string      { return "fake" }
func (e *realGateEngine) BuildProfile() string { return "test" }
func (e *realGateEngine) Running() bool        { return e.running }
func (e *realGateEngine) ConfigurePolicy(context.Context, string, engine.LimitSet) error {
	return nil
}
func (e *realGateEngine) BlockAccount(context.Context, domain.AccountID, string) error { return nil }
func (e *realGateEngine) UnblockAccount(context.Context, domain.AccountID) error       { return nil }
func (e *realGateEngine) ApplyAccountAdjustment(
	context.Context, domain.AccountID, domain.AdjustmentRequest,
) (engine.AdjustmentResult, error) {
	return engine.AdjustmentResult{Accepted: &domain.AdjustmentOutcomeAccepted{}}, nil
}
func (e *realGateEngine) ApplyAccountAdjustmentBatch(
	_ context.Context, _ domain.AccountID, reqs []domain.AdjustmentRequest,
) ([]engine.AdjustmentResult, *engine.AdjustmentBatchReject, error) {
	out := make([]engine.AdjustmentResult, 0, len(reqs))
	for range reqs {
		out = append(out, engine.AdjustmentResult{Accepted: &domain.AdjustmentOutcomeAccepted{}})
	}
	return out, nil, nil
}
func (e *realGateEngine) SubmitOrder(context.Context, domain.Order) (engine.OrderResult, error) {
	return engine.OrderResult{Accepted: true}, nil
}
func (e *realGateEngine) ReserveHold(context.Context, domain.Order) (engine.HoldResult, error) {
	return engine.HoldResult{Accepted: true}, nil
}
func (e *realGateEngine) CommitHeld(context.Context, string) error   { return nil }
func (e *realGateEngine) RollbackHeld(context.Context, string) error { return nil }
func (e *realGateEngine) SubmitImmediate(
	context.Context, domain.Order,
) (engine.ImmediateResult, error) {
	return engine.ImmediateResult{Accepted: true}, nil
}
func (e *realGateEngine) SetReservationStore(engine.ReservationStore) {}
func (e *realGateEngine) ReconcileOrphans(context.Context) (int, error) {
	return 0, nil
}
func (e *realGateEngine) RunAccountSynchronized(
	_ context.Context, _ domain.AccountID, fn func(engine.AccountLane) error,
) error {
	return fn(e)
}
func (e *realGateEngine) RunGroupSynchronized(
	_ context.Context, _ string, fn func(engine.GroupLane) error,
) error {
	return fn(e)
}
func (e *realGateEngine) ApplyExecutionReport(
	_ context.Context, in domain.ExecutionReportInput,
) (engine.ExecutionReportResult, error) {
	// Only reached on the force path (the guard rejects the rejected case earlier).
	// Return a valid single-fill persistence so the node's settlement write set is
	// well-formed and the report completes.
	persistence := engine.ExecutionReportPersistence{
		OrderStatus: in.OrderStatus,
		Leaves:      in.LeavesQuantity,
		Events: []domain.OrderEvent{{
			Order: in.Order,
			Type:  domain.OrderEventFill,
		}},
	}
	return engine.ExecutionReportResult{Persistence: &persistence}, nil
}
func (e *realGateEngine) RegisterGroup(context.Context, []domain.AccountID, string) error {
	return nil
}
func (e *realGateEngine) UnregisterGroup(context.Context, []domain.AccountID, string) error {
	return nil
}
func (e *realGateEngine) BlockGroup(context.Context, string, string) error { return nil }
func (e *realGateEngine) UnblockGroup(context.Context, string) error       { return nil }
func (e *realGateEngine) CheckOrder(
	context.Context, domain.OrderProbe,
) (domain.CheckResult, error) {
	return domain.CheckResult{}, nil
}
func (e *realGateEngine) MarketDataSink() marketdata.Sink { return realGateSink{} }
func (e *realGateEngine) Stop()                           { e.running = false }

type realGateSink struct{}

func (realGateSink) Push(marketdata.QuoteUpdate) error { return nil }
