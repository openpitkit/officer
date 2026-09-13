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
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	openpit "go.openpit.dev/openpit"
	"go.openpit.dev/openpit/accountadjustment"
	"go.openpit.dev/openpit/asyncengine"
	"go.openpit.dev/openpit/configure"
	"go.openpit.dev/openpit/model"
	"go.openpit.dev/openpit/param"
	"go.openpit.dev/openpit/pretrade"
	"go.openpit.dev/openpit/pretrade/policies"
	"go.openpit.dev/openpit/reject"

	officerengine "go.openpit.dev/officer/engine"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/node"
	fwsigning "go.openpit.dev/officer/framework/signing"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/internal/store/sqlite"
	appsigning "go.openpit.dev/officer/signing"
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
		Account:          acct,
		Source:           domain.SourceAPI,
		Principal:        domain.PrincipalOperator,
		BaseAsset:        "AAPL",
		QuoteAsset:       "USD",
		Side:             domain.OrderSideBuy,
		AmountKind:       domain.OrderAmountKindQuantity,
		AmountValue:      "2",
		Price:            "400",
		Status:           domain.OrderStatusFilled,
		ReservedQuantity: "0",
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

// TestApplyExecutionReport_RealCommissionSignedAndReproduced drives a fill
// carrying a structured commission in its own currency through the REAL
// execution-report path (real backend.Service, localNode, SQLite store, and
// Ed25519 signer) and proves the commission reaches the signed bytes in both
// attestation-facing flows: the freshly-settled attestation token returned by
// the POST, and the replay-from-event reconstruction the reproduction endpoint
// serves from the persisted event. It also asserts the OrderEventPayload
// commission round-trips through the store's JSON payload blob (no dedicated
// column).
func TestApplyExecutionReport_RealCommissionSignedAndReproduced(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	handler, realm := newRealServiceRouter(t)

	const acct domain.AccountID = "acc-1"
	seedRealAccountAndAssets(t, realm, acct)
	order, err := realm.CreateOrder(ctx, domain.Order{
		Account:          acct,
		Source:           domain.SourceAPI,
		Principal:        domain.PrincipalOperator,
		BaseAsset:        "AAPL",
		QuoteAsset:       "USD",
		Side:             domain.OrderSideBuy,
		AmountKind:       domain.OrderAmountKindQuantity,
		AmountValue:      "10",
		Price:            "150",
		Status:           domain.OrderStatusSubmitted,
		ReservedQuantity: "10",
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	path := "/api/v1/orders/" + order.ExternalID.String() + "/execution-reports"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path,
		bytes.NewBufferString(
			`{"quantity":"3.5","price":"150.20","leavesQuantity":"6.5",`+
				`"status":"partially_filled",`+
				`"commission":{"amount":"-0.30","currency":"USDT"}}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("execution report: status = %d, want 201; body=%s",
			rec.Code, rec.Body.String())
	}

	// Fresh-settlement path: decode the token the settlement just signed and
	// confirm its canonical bytes bind the structured commission.
	fresh := bodyMap(t, rec.Result())
	token, _ := fresh["attestationToken"].(string)
	if token == "" {
		t.Fatalf("want attestationToken, got %v", fresh["attestationToken"])
	}
	env, err := fwsigning.DecodeEnvelope(token)
	if err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if env.Approval.Result == nil || env.Approval.Result.Commission == nil {
		t.Fatalf("fresh attestation missing commission: %+v", env.Approval.Result)
	}
	if env.Approval.Result.Commission.Amount != "-0.30" ||
		env.Approval.Result.Commission.Currency != "USDT" {
		t.Fatalf("fresh commission = %+v, want -0.30/USDT",
			env.Approval.Result.Commission)
	}
	if env.Approval.ExecutionReport == nil ||
		env.Approval.ExecutionReport.FillQuantity != "3.5" ||
		env.Approval.ExecutionReport.FillPrice != "150.20" ||
		env.Approval.ExecutionReport.LeavesQuantity != "6.5" ||
		env.Approval.ExecutionReport.Order != order.ExternalID {
		t.Fatalf("fresh attestation missing original report: %+v",
			env.Approval.ExecutionReport)
	}
	canon, err := fwsigning.CanonicalBytes(env.Approval)
	if err != nil {
		t.Fatalf("canonical bytes: %v", err)
	}
	if !strings.Contains(string(canon),
		`"commission":{"amount":"-0.30","currency":"USDT"}`) {
		t.Fatalf("fresh canonical bytes missing commission:\n%s", canon)
	}

	// The commission persisted on the fill event's JSON payload blob must round-trip
	// through the store unchanged (no dedicated column).
	events, err := realm.ListOrderEvents(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("ListOrderEvents: %v", err)
	}
	var fill domain.OrderEvent
	for _, ev := range events {
		if ev.Type == domain.OrderEventFill {
			fill = ev
		}
	}
	if fill.ExternalID.IsZero() {
		t.Fatalf("no fill event recorded; events=%+v", events)
	}
	if fill.Payload.Commission == nil ||
		fill.Payload.Commission.Amount != "-0.30" ||
		fill.Payload.Commission.Currency != "USDT" {
		t.Fatalf("fill event payload commission did not round-trip: %+v",
			fill.Payload.Commission)
	}
	if fill.Payload.ExecutionReport == nil ||
		fill.Payload.ExecutionReport.FillQuantity != "3.5" ||
		fill.Payload.ExecutionReport.FillPrice != "150.20" ||
		fill.Payload.ExecutionReport.LeavesQuantity != "6.5" ||
		fill.Payload.ExecutionReport.Order != order.ExternalID ||
		fill.Payload.ExecutionReport.Account != "" ||
		fill.Payload.ExecutionReport.BaseAsset != "" ||
		fill.Payload.ExecutionReport.QuoteAsset != "" ||
		fill.Payload.ExecutionReport.Side != "" ||
		fill.Payload.ExecutionReport.Force {
		t.Fatalf("fill event did not preserve original HTTP report: %+v",
			fill.Payload.ExecutionReport)
	}

	// Replay-from-event reconstruction path: the reproduction bundle decodes the
	// persisted token and surfaces the bound commission both in the byte-identical
	// canonicalApproval and in the reconstructed request result DTO.
	reproPath := "/api/v1/orders/" + order.ExternalID.String() +
		"/events/" + fill.ExternalID.String() + "/reproduction"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, reproPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("reproduction: status = %d, want 200; body=%s",
			rec.Code, rec.Body.String())
	}
	repro := bodyMap(t, rec.Result())
	reproCanon, _ := repro["canonicalApproval"].(string)
	if !strings.Contains(reproCanon,
		`"commission":{"amount":"-0.30","currency":"USDT"}`) {
		t.Fatalf("reproduction canonicalApproval missing commission:\n%s", reproCanon)
	}
	request, _ := repro["request"].(map[string]any)
	result, _ := request["result"].(map[string]any)
	commission, ok := result["commission"].(map[string]any)
	if !ok {
		t.Fatalf("reproduction request.result.commission missing: %v",
			result["commission"])
	}
	if commission["amount"] != "-0.30" || commission["currency"] != "USDT" {
		t.Fatalf("reproduction request.result.commission = %v, want -0.30/USDT",
			commission)
	}
}

// TestApplyExecutionReport_RealWorkflowCarriesRecordedReservation proves an R3
// report records caller leaves while sending the recorded reservation remainder
// to the engine.
func TestApplyExecutionReport_RealWorkflowCarriesRecordedReservation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	handler, realm, eng, _ := newRealServiceRouterWithEngine(t)

	const account domain.AccountID = "acc-1"
	seedRealAccountAndAssets(t, realm, account)
	order, err := realm.CreateOrder(ctx, domain.Order{
		Account:          account,
		Source:           domain.SourceAPI,
		Principal:        domain.PrincipalOperator,
		BaseAsset:        "AAPL",
		QuoteAsset:       "USD",
		Side:             domain.OrderSideBuy,
		AmountKind:       domain.OrderAmountKindQuantity,
		AmountValue:      "10",
		Price:            "150",
		Status:           domain.OrderStatusSubmitted,
		Leaves:           "10",
		ReservedQuantity: "10",
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost,
		"/api/v1/orders/"+order.ExternalID.String()+"/execution-reports",
		bytes.NewBufferString(
			`{"status":"accepted","leavesQuantity":"6.500",`+
				`"commission":{"amount":"-0.30","currency":"USD"}}`,
		),
	))
	if rec.Code != http.StatusCreated {
		t.Fatalf("workflow report: status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	if len(eng.executionReportLeaves) != 1 || eng.executionReportLeaves[0] != "10" {
		t.Fatalf(
			"engine reservation remainder = %+v, want 10",
			eng.executionReportLeaves,
		)
	}
	detail, err := realm.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Leaves != "6.500" {
		t.Fatalf("recorded leaves = %q, want caller value 6.500", detail.Order.Leaves)
	}
	if len(detail.Events) != 1 || detail.Events[0].Payload.ExecutionReport == nil ||
		detail.Events[0].Payload.ExecutionReport.LeavesQuantity != "6.500" {
		t.Fatalf("event request leaves = %+v, want caller value 6.500", detail.Events)
	}
}

// TestSubmitOrder_RealStoreRequiresRegisteredCallerPrincipal proves the caller
// principal copied onto an order must exist in the store dictionary.
func TestSubmitOrder_RealStoreRequiresRegisteredCallerPrincipal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, realm, _, service := newRealServiceRouterWithEngine(t)

	const account domain.AccountID = "acc-1"
	seedRealAccountAndAssets(t, realm, account)
	order := domain.Order{
		Account:     account,
		ExternalID:  "operator-principal-order",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "1",
		Price:       "150",
	}
	submitted, err := service.SubmitOrder(
		ctx,
		order,
		domain.MissingAccountReject,
		domain.Caller{
			Source:    domain.SourceMCP,
			Principal: domain.PrincipalOperator,
		},
	)
	if err != nil {
		t.Fatalf("operator submission: %v", err)
	}
	detail, err := realm.GetOrder(ctx, submitted.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder(operator submission): %v", err)
	}
	if detail.Order.Principal != domain.PrincipalOperator {
		t.Fatalf(
			"stored principal = %q, want %q",
			detail.Order.Principal,
			domain.PrincipalOperator,
		)
	}

	const missingPrincipal = "principal-not-in-dictionary"
	order.ExternalID = "missing-principal-order"
	_, err = service.SubmitOrder(
		ctx,
		order,
		domain.MissingAccountReject,
		domain.Caller{
			Source:    domain.SourceMCP,
			Principal: missingPrincipal,
		},
	)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("missing-principal submission error = %v, want ErrInvalid", err)
	}
	if _, getErr := realm.GetOrder(ctx, order.ExternalID); !errors.Is(
		getErr,
		domain.ErrNotFound,
	) {
		t.Fatalf(
			"GetOrder(missing-principal submission) = %v, want ErrNotFound",
			getErr,
		)
	}
}

// newRealServiceRouter builds the full production chain a handler test would
// otherwise stub: a real SQLite store, a real localNode over an engine fake, a
// real backend.Service, and the real HTTP router. It returns
// the mounted handler and the bound realm store for direct seeding.
func newRealServiceRouter(t *testing.T) (http.Handler, store.RealmStore) {
	t.Helper()
	handler, realm, _, _ := newRealServiceRouterWithEngine(t)
	return handler, realm
}

func newRealServiceRouterWithEngine(t *testing.T) (
	http.Handler,
	store.RealmStore,
	*realGateEngine,
	node.Node,
) {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.New(t.TempDir()+"/httpapi-realgate.db", domain.DefaultRealm)
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	realm, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	driver, err := openpit.NewEngineBuilder().
		AccountSync().
		Builtin(policies.BuildOrderValidation()).
		Build()
	if err != nil {
		t.Fatalf("build chain driver: %v", err)
	}
	async, err := asyncengine.NewBuilder(driver).Sharded(1).Build()
	if err != nil {
		driver.Stop()
		t.Fatalf("build async engine: %v", err)
	}
	eng := &realGateEngine{
		running: true,
		async:   async,
		driver:  driver,
		assets:  make(map[string]domain.EngineAssetID),
		groups:  make(map[string]domain.EngineGroupID),
	}
	n, _, err := node.NewLocalNode(ctx, domain.DefaultRealm, st, func(engine.Snapshot) (engine.Engine, error) {
		return eng, nil
	}, func(err error) { t.Errorf("unexpected fatal shutdown: %v", err) })
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	signer, err := appsigning.New(realm, appsigning.NewMemoryReplayGuard())
	if err != nil {
		t.Fatalf("signing.New: %v", err)
	}
	if _, err := signer.GenerateKey(ctx); err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	svc, err := backend.New(n, nil, signer, officerengine.LockSettlementPrice)
	if err != nil {
		t.Fatalf("backend.New: %v", err)
	}
	handler, err := newRouter(svc)
	if err != nil {
		t.Fatalf("newRouter: %v", err)
	}
	return handler, realm, eng, n
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
		if _, err := realm.CreateAsset(ctx, domain.Asset{Code: code}); err != nil {
			t.Fatalf("CreateAsset(%s): %v", code, err)
		}
	}
}

// realGateEngine is a minimal engine.Engine fake sufficient for the
// execution-report terminal-gate test: on the force path it returns a valid
// single-fill persistence write-set so the node
// completes the settlement. It carries no risk logic; the gate under test is the
// node's own terminal-order guard, not the engine.
type realGateEngine struct {
	running               bool
	executionReportLeaves []string
	async                 *asyncengine.AsyncEngine
	driver                *openpit.Engine
	assets                map[string]domain.EngineAssetID
	groups                map[string]domain.EngineGroupID
}

func (e *realGateEngine) AsyncEngine() *asyncengine.AsyncEngine { return e.async }

func (*realGateEngine) AccountID(
	domain.AccountID,
) (param.AccountID, error) {
	return param.NewAccountIDFromUint64(1), nil
}

func (e *realGateEngine) ExecutionReportModel(
	in domain.ExecutionReportInput,
	leavesQuantity string,
) (model.ExecutionReport, error) {
	order, err := e.OrderModel(domain.Order{
		Account:     in.Account,
		BaseAsset:   in.BaseAsset,
		QuoteAsset:  in.QuoteAsset,
		Side:        in.Side,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "1",
	})
	if err != nil {
		return model.ExecutionReport{}, err
	}
	orderOperation, _ := order.Operation().Get()
	instrument, _ := orderOperation.Instrument().Get()
	accountID, _ := orderOperation.AccountID().Get()
	side, _ := orderOperation.Side().Get()
	report := model.NewExecutionReport()
	operation := report.EnsureOperationView()
	operation.SetInstrument(instrument)
	operation.SetAccountID(accountID)
	operation.SetSide(side)
	fill := report.EnsureFillView()
	if in.FillQuantity != "" && in.FillPrice != "" {
		price, err := param.NewPriceFromString(in.FillPrice)
		if err != nil {
			return model.ExecutionReport{}, err
		}
		quantity, err := param.NewQuantityFromString(in.FillQuantity)
		if err != nil {
			return model.ExecutionReport{}, err
		}
		fill.SetLastTrade(model.NewExecutionReportTrade(price, quantity))
	}
	if leavesQuantity != "" {
		leaves, err := param.NewQuantityFromString(leavesQuantity)
		if err != nil {
			return model.ExecutionReport{}, err
		}
		fill.SetRemainingReservedQuantity(leaves)
	}
	fill.SetIsFinal(domain.OrderStatusTerminal(in.OrderStatus))
	if len(in.Lock) > 0 {
		fill.SetLock(in.Lock)
	}
	engineLeaves := ""
	if reportFill, ok := report.Fill().Get(); ok {
		if leaves, ok := reportFill.RemainingReservedQuantity().Get(); ok {
			engineLeaves = leaves.String()
		}
	}
	e.executionReportLeaves = append(e.executionReportLeaves, engineLeaves)
	return report, nil
}

func (e *realGateEngine) SettledExecutionReport(
	in domain.ExecutionReportInput,
	_ param.AccountID,
	_ pretrade.PostTradeResult,
) (engine.ExecutionReportResult, error) {
	return e.materializeExecutionReport(in)
}

func (*realGateEngine) AccountAdjustmentModels(
	reqs []domain.AdjustmentRequest,
) ([]model.AccountAdjustment, error) {
	return make([]model.AccountAdjustment, len(reqs)), nil
}

func (*realGateEngine) AppliedAccountAdjustmentBatch(
	_ domain.AccountID,
	reqs []domain.AdjustmentRequest,
	_ accountadjustment.BatchResult,
) ([]engine.AdjustmentResult, *domain.AdjustmentOutcomeRejected, error) {
	results := make([]engine.AdjustmentResult, 0, len(reqs))
	for range reqs {
		results = append(results, engine.AdjustmentResult{
			Accepted: &domain.AdjustmentOutcomeAccepted{},
		})
	}
	return results, nil, nil
}

func (*realGateEngine) OrderModel(order domain.Order) (model.Order, error) {
	base, err := param.NewAsset(order.BaseAsset)
	if err != nil {
		return model.Order{}, err
	}
	quote, err := param.NewAsset(order.QuoteAsset)
	if err != nil {
		return model.Order{}, err
	}
	var side param.Side
	switch order.Side {
	case domain.OrderSideBuy:
		side = param.SideBuy
	case domain.OrderSideSell:
		side = param.SideSell
	default:
		return model.Order{}, domain.ErrInvalid
	}
	var amount param.TradeAmount
	switch order.AmountKind {
	case domain.OrderAmountKindQuantity:
		quantity, quantityErr := param.NewQuantityFromString(order.AmountValue)
		if quantityErr != nil {
			return model.Order{}, quantityErr
		}
		amount = param.NewQuantityTradeAmount(quantity)
	case domain.OrderAmountKindVolume:
		volume, volumeErr := param.NewVolumeFromString(order.AmountValue)
		if volumeErr != nil {
			return model.Order{}, volumeErr
		}
		amount = param.NewVolumeTradeAmount(volume)
	default:
		return model.Order{}, domain.ErrInvalid
	}
	result := model.NewOrder()
	operation := result.EnsureOperationView()
	operation.SetInstrument(param.NewInstrument(base, quote))
	operation.SetAccountID(param.NewAccountIDFromUint64(1))
	operation.SetSide(side)
	operation.SetTradeAmount(amount)
	if order.Price != "" {
		price, priceErr := param.NewPriceFromString(order.Price)
		if priceErr != nil {
			return model.Order{}, priceErr
		}
		operation.SetPrice(price)
	}
	return result, nil
}

func (e *realGateEngine) CheckOrderModel(probe domain.OrderProbe) (model.Order, error) {
	return e.OrderModel(domain.Order{
		Account:     probe.Account,
		BaseAsset:   probe.BaseAsset,
		QuoteAsset:  probe.QuoteAsset,
		Side:        probe.Side,
		AmountKind:  probe.AmountKind,
		AmountValue: probe.AmountValue,
		Price:       probe.Price,
	})
}

func (*realGateEngine) RejectedOrder(domain.Order, []reject.Reject) engine.OrderResult {
	return engine.OrderResult{}
}

func (*realGateEngine) ReservedOrder(
	order domain.Order, _ asyncengine.OperationResult,
) (engine.OrderResult, error) {
	return engine.OrderResult{
		Accepted:            true,
		SettlementLockPrice: order.Price,
	}, nil
}

func (e *realGateEngine) AppliedDropCopyOrder(
	order domain.Order, result asyncengine.DropCopyResult,
) (engine.OrderResult, error) {
	return e.ReservedOrder(order, result)
}

func (*realGateEngine) RejectedImmediate(
	domain.Order, []reject.Reject,
) engine.ImmediateResult {
	return engine.ImmediateResult{}
}

func (*realGateEngine) PrepareImmediateReservation(
	domain.Order, asyncengine.OperationResult,
) (engine.ImmediatePreparation, error) {
	return engine.ImmediatePreparation{}, domain.ErrNotImplemented
}

func (*realGateEngine) PrepareImmediateDropCopy(
	domain.Order, asyncengine.DropCopyResult,
) (engine.ImmediatePreparation, error) {
	return engine.ImmediatePreparation{}, domain.ErrNotImplemented
}

func (*realGateEngine) SettleImmediate(
	domain.Order, engine.ImmediatePreparation, pretrade.PostTradeResult,
) (engine.ImmediateResult, error) {
	return engine.ImmediateResult{}, domain.ErrNotImplemented
}

func (*realGateEngine) CheckedOrder(
	domain.OrderProbe, asyncengine.OrderCheckResult,
) (domain.CheckResult, error) {
	return domain.CheckResult{}, domain.ErrNotImplemented
}

func (e *realGateEngine) Version() string                            { return "fake" }
func (e *realGateEngine) BuildProfile() string                       { return "test" }
func (e *realGateEngine) Running() bool                              { return e.running }
func (*realGateEngine) AddAccountResolverEntry(domain.Account) error { return nil }
func (e *realGateEngine) AddAssetResolverEntry(asset domain.Asset) error {
	if e.assets == nil {
		e.assets = make(map[string]domain.EngineAssetID)
	}
	e.assets[asset.Code] = asset.EngineAssetID
	return nil
}
func (e *realGateEngine) ResolveAsset(code string) (param.Asset, error) {
	id, ok := e.assets[code]
	if !ok {
		return param.Asset{}, domain.ErrInvalid
	}
	return param.NewAsset(strconv.FormatUint(id.Uint64(), 10))
}
func (*realGateEngine) RenameAccountResolverEntry(
	domain.AccountID,
	domain.Account,
) error {
	return nil
}
func (*realGateEngine) RenameAssetResolverEntry(string, domain.Asset) error { return nil }
func (*realGateEngine) RemoveAssetResolverEntry(domain.Asset) error         { return nil }
func (e *realGateEngine) AddGroupResolverEntry(group domain.AccountGroup) error {
	if e.groups == nil {
		e.groups = make(map[string]domain.EngineGroupID)
	}
	e.groups[group.Code] = group.EngineGroupID
	return nil
}
func (e *realGateEngine) ResolveGroup(code string) (param.AccountGroupID, error) {
	if code == "" {
		return param.DefaultAccountGroup, nil
	}
	id, ok := e.groups[code]
	if !ok {
		return param.AccountGroupID{}, domain.ErrInvalid
	}
	return param.NewAccountGroupIDFromUint32(id.Uint32())
}
func (*realGateEngine) RenameGroupResolverEntry(
	string,
	domain.AccountGroup,
) error {
	return nil
}
func (*realGateEngine) RemoveGroupResolverEntry(domain.AccountGroup) error { return nil }
func (e *realGateEngine) ConfigurePolicy(
	context.Context, string, engine.LimitSet,
) (engine.PolicyConfigurationResult, error) {
	return engine.PolicyConfigurationResult{}, nil
}
func (*realGateEngine) SpotFundsAccountPnlAssignment(
	pnl string,
	haltReason domain.PnlHaltReason,
) (asyncengine.SpotFundsAccountPnlAssignment, error) {
	if (pnl == "") == (haltReason == "") {
		return asyncengine.SpotFundsAccountPnlAssignment{}, domain.ErrInvalid
	}
	if err := domain.ValidatePnlHaltReason(haltReason); err != nil {
		return asyncengine.SpotFundsAccountPnlAssignment{}, err
	}
	if pnl != "" {
		value, err := param.NewPnlFromString(pnl)
		if err != nil {
			return asyncengine.SpotFundsAccountPnlAssignment{}, err
		}
		return asyncengine.SpotFundsAccountPnlAssignment{
			PolicyName: policies.SpotFundsPolicyName,
			State:      model.NewPnlState(value),
		}, nil
	}
	return asyncengine.SpotFundsAccountPnlAssignment{}, domain.ErrInvalid
}
func (*realGateEngine) AppliedSpotFundsAccountPnl(
	domain.AccountID,
	string,
	domain.PnlHaltReason,
	configure.PolicyConfigurationResult,
) ([]domain.AccountBlock, error) {
	return nil, nil
}
func (e *realGateEngine) materializeExecutionReport(
	in domain.ExecutionReportInput,
) (engine.ExecutionReportResult, error) {
	// Mirror the native settlement builder (executionReportPersistenceFrom): copy
	// the report-owned fill fields and structured commission into persistence and
	// the event payload; a fill also copies them onto the persisted trade.
	// This fake carries no risk logic; it only shapes a well-formed write set so
	// the node completes the settlement.
	payload := domain.OrderEventPayload{
		FillQuantity:   in.FillQuantity,
		FillPrice:      in.FillPrice,
		FillLockPrice:  in.LockPrice,
		LeavesQuantity: in.LeavesQuantity,
		OrderStatus:    string(in.OrderStatus),
		Commission:     in.Commission,
	}
	persistence := engine.ExecutionReportPersistence{
		OrderStatus: in.OrderStatus,
		Commission:  in.Commission,
		Leaves:      in.LeavesQuantity,
		Events: []domain.OrderEvent{{
			Order:   in.Order,
			Type:    domain.OrderEventFill,
			Payload: payload,
		}},
	}
	if in.FillQuantity != "" && in.FillPrice != "" {
		persistence.Trade = &domain.Trade{
			Order:      in.Order,
			Account:    in.Account,
			BaseAsset:  in.BaseAsset,
			QuoteAsset: in.QuoteAsset,
			Side:       in.Side,
			Quantity:   in.FillQuantity,
			Price:      in.FillPrice,
			LockPrice:  in.LockPrice,
			Commission: in.Commission,
		}
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

func (e *realGateEngine) SetGroupCurrency(context.Context, string, string) error { return nil }

func (e *realGateEngine) ClearGroupCurrency(context.Context, string) error { return nil }
func (e *realGateEngine) MarketDataSink() marketdata.Sink                  { return realGateSink{} }
func (e *realGateEngine) CloseMarketDataService()                          {}

func (e *realGateEngine) Stop() {
	e.running = false
	if e.async != nil {
		_ = e.async.StopGraceful(context.Background())
		e.async = nil
	}
	if e.driver != nil {
		e.driver.Stop()
		e.driver = nil
	}
}

type realGateSink struct{}

func (realGateSink) Push(marketdata.QuoteUpdate) error { return nil }
