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

package officer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"go.openpit.dev/officer/engine"
	"go.openpit.dev/officer/framework/alerts"
	frameworkapp "go.openpit.dev/officer/framework/app"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	fwengine "go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/internal/accountmode"
)

func orderReviewAlertConfig(t *testing.T, service **alerts.Service) Config {
	t.Helper()
	return Config{
		SQLitePath: filepath.Join(t.TempDir(), "review-alerts.db"),
		InitialSetup: &InitialSetup{
			AccountMode: accountmode.ModeMulti, FundsLimitMode: domain.SpotFundsLimitModeTrackOnly,
			Encryption: "disabled", Signing: "disabled", Access: "open",
		},
		AlertServiceReady: func(s *alerts.Service) error { *service = s; return nil },
	}
}

func orderReviewAlertApp(t *testing.T) (backend.ControlPlane, *alerts.Service, context.Context) {
	t.Helper()
	var service *alerts.Service
	app, _ := serveSeededApp(t, orderReviewAlertConfig(t, &service), nil)
	ctx := auth.ContextWithCaller(t.Context(), domain.Caller{Source: domain.SourceAPI, Principal: domain.PrincipalOperator})
	return app.Service(), service, ctx
}

func prepareOrderReviewAlert(t *testing.T, cp backend.ControlPlane, ctx context.Context, ruleMode domain.RuleMode) domain.Order {
	t.Helper()
	if _, err := cp.ApplyAdjustment(ctx, "other", "", domain.AdjustmentRequest{
		Type: domain.AdjustmentTypeDeposit, Asset: "USD",
		Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: "10000"},
	}, domain.MissingAccountReject); err != nil {
		t.Fatal(err)
	}
	if _, err := cp.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
		Mode: ruleMode, Scope: domain.ScopeAccountUnderlyingAsset, Account: "other", Asset: "AAPL", MaxQuantity: "1",
	}, domain.LimitWriteReplace, domain.MissingAccountReject); err != nil {
		t.Fatal(err)
	}
	return domain.Order{
		Account: "other", BaseAsset: "AAPL", QuoteAsset: "USD", Side: domain.OrderSideBuy,
		AmountKind: domain.OrderAmountKindQuantity, AmountValue: "2", Price: "100",
	}
}

func assertOrderReviewAlerts(t *testing.T, service *alerts.Service, ctx context.Context, id, markCode string) {
	t.Helper()
	listed, err := service.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := 0
	if id != "" {
		want = 1
	}
	if listed.Total != want || len(listed.Alerts) != want {
		t.Fatalf("open alerts = %+v, want %d", listed, want)
	}
	if want == 0 {
		return
	}
	if actions := listed.Alerts[0].Actions; !reflect.DeepEqual(actions, []string{"close"}) {
		t.Fatalf("actions = %+v, want [close]", actions)
	}
	event := listed.Alerts[0].Event
	var payload map[string]string
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	wantPayload := map[string]string{"order": id, "account": "other", "markCode": markCode}
	if event.ID != id || event.Type != orderAwaitingReviewAlert || !reflect.DeepEqual(payload, wantPayload) {
		t.Fatalf("event = %+v, payload = %+v, want %+v", event, payload, wantPayload)
	}
	if event.Source != domain.SourceAPI || event.At.IsZero() || event.At.Location() != time.UTC {
		t.Fatalf("publication source/time = %+v", event)
	}
}

func assertOrderReviewResolution(t *testing.T, cp backend.ControlPlane, ctx context.Context, id string, caller domain.Caller) {
	t.Helper()
	rows, err := cp.ListAudit(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	row := auditRowForAction(t, rows, domain.AuditActionResolveAlert)
	if row.Actor != caller.Principal || row.Source != caller.Source || row.Detail.Params["alert"] != id || row.Detail.Params["action"] != "close" || row.Detail.Params["result"] != `{"closed":true}` {
		t.Fatalf("resolution audit = %+v, want actor %+v for %s", row, caller, id)
	}
}

func TestOrderReviewAlertSubmission(t *testing.T) {
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		for _, ruleMode := range []domain.RuleMode{domain.RuleModeReviewWithoutHold, domain.RuleModeReviewWithHold, domain.RuleModeReject} {
			t.Run(fmt.Sprintf("%s/%s", mode, ruleMode), func(t *testing.T) {
				cp, service, ctx := orderReviewAlertApp(t)
				order := prepareOrderReviewAlert(t, cp, ctx, ruleMode)
				assertOrderReviewAlerts(t, service, ctx, "", "")
				approval, err := cp.SubmitOrder(ctx, order, mode, domain.MissingAccountReject)
				if err != nil || approval.AlertFailure != nil {
					t.Fatalf("submit = %+v, %v", approval, err)
				}
				if ruleMode == domain.RuleModeReject {
					if approval.Verdict != domain.VerdictReject {
						t.Fatalf("submit verdict = %s", approval.Verdict)
					}
					assertOrderReviewAlerts(t, service, ctx, "", "")
					return
				}
				if approval.Verdict != domain.VerdictReview || len(approval.Reviews) == 0 {
					t.Fatalf("submit = %+v", approval)
				}
				assertOrderReviewAlerts(t, service, ctx, approval.OrderExternalID, approval.Reviews[0].Code)
				stored, err := cp.GetOrder(ctx, approval.OrderExternalID)
				if err != nil || stored.Order.Status != domain.OrderStatusAwaitingReview {
					t.Fatalf("waiting order = %+v, %v", stored, err)
				}
				producer := orderReviewAlertProducer{service: service}
				caller, err := auth.CallerFromContext(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if err := producer.OrderAwaitingReview(ctx, stored.Order, approval.Reviews[0], caller); err == nil {
					t.Fatal("duplicate publication succeeded")
				}
				assertOrderReviewAlerts(t, service, ctx, approval.OrderExternalID, approval.Reviews[0].Code)
			})
		}
		t.Run(mode+"/accept", func(t *testing.T) {
			cp, service, ctx := orderReviewAlertApp(t)
			order := prepareOrderReviewAlert(t, cp, ctx, domain.RuleModeReject)
			order.AmountValue = "1"
			approval, err := cp.SubmitOrder(ctx, order, mode, domain.MissingAccountReject)
			if err != nil || approval.Verdict != domain.VerdictAccept || approval.AlertFailure != nil {
				t.Fatalf("submit = %+v, %v", approval, err)
			}
			assertOrderReviewAlerts(t, service, ctx, "", "")
		})
	}
}

func TestOrderReviewAlertDecisions(t *testing.T) {
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		for _, ruleMode := range []domain.RuleMode{domain.RuleModeReviewWithoutHold, domain.RuleModeReviewWithHold} {
			for _, action := range []string{"confirm", "reject", "force", "cancel"} {
				if action == "force" && ruleMode != domain.RuleModeReviewWithHold {
					continue
				}
				t.Run(fmt.Sprintf("%s/%s/%s", mode, ruleMode, action), func(t *testing.T) {
					cp, service, ctx := orderReviewAlertApp(t)
					order := prepareOrderReviewAlert(t, cp, ctx, ruleMode)
					approval, err := cp.SubmitOrder(ctx, order, mode, domain.MissingAccountReject)
					if err != nil || approval.Verdict != domain.VerdictReview || len(approval.Reviews) == 0 || approval.AlertFailure != nil {
						t.Fatalf("submit = %+v, %v", approval, err)
					}
					id, markCode := approval.OrderExternalID, approval.Reviews[0].Code
					assertOrderReviewAlerts(t, service, ctx, id, markCode)
					caller := domain.Caller{Source: domain.SourcePanel, Principal: domain.PrincipalOperator}
					deciding := auth.ContextWithCaller(t.Context(), caller)
					if action == "reject" {
						if _, err := cp.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
							Mode: domain.RuleModeReject, Scope: domain.ScopeAccountUnderlyingAsset, Account: "other", Asset: "AAPL", MaxQuantity: "1",
						}, domain.LimitWriteReplace, domain.MissingAccountReject); err != nil {
							t.Fatal(err)
						}
					}
					var decided domain.Order
					var failure *backend.AlertFailure
					var verdict domain.Verdict
					switch action {
					case "confirm", "reject":
						var result backend.VenueSendResult
						decided, result, err = cp.ConfirmOrderReview(deciding, id)
						failure, verdict = result.Approval.AlertFailure, result.Approval.Verdict
					case "force":
						var result backend.VenueSendResult
						decided, result, err = cp.ForceOrderReview(deciding, id)
						failure, verdict = result.Approval.AlertFailure, result.Approval.Verdict
					case "cancel":
						decided, _, failure, err = cp.CancelOrderReview(deciding, id)
					}
					if err != nil || failure != nil {
						t.Fatalf("%s = %+v, alert failure %+v, %v", action, decided, failure, err)
					}
					if action == "reject" {
						if verdict != domain.VerdictReject || decided.Status != domain.OrderStatusAwaitingReview {
							t.Fatalf("rejected confirm = %+v, verdict %s", decided, verdict)
						}
						assertOrderReviewAlerts(t, service, deciding, id, markCode)
						rows, err := cp.ListAudit(deciding, 100)
						if err != nil {
							t.Fatal(err)
						}
						for _, row := range rows {
							if row.Action == domain.AuditActionResolveAlert {
								t.Fatalf("rejected confirm resolved alert: %+v", row)
							}
						}
						return
					}
					if decided.Status == domain.OrderStatusAwaitingReview || (action != "cancel" && verdict != domain.VerdictAccept) {
						t.Fatalf("decision = %+v, verdict %s", decided, verdict)
					}
					assertOrderReviewAlerts(t, service, deciding, "", "")
					assertOrderReviewResolution(t, cp, deciding, id, caller)
				})
			}
		}
	}
}

func TestOrderReviewAlertOperatorClose(t *testing.T) {
	cp, service, ctx := orderReviewAlertApp(t)
	order := prepareOrderReviewAlert(t, cp, ctx, domain.RuleModeReviewWithoutHold)
	approval, err := cp.SubmitOrder(ctx, order, domain.SubmitModeHold, domain.MissingAccountReject)
	if err != nil || approval.Verdict != domain.VerdictReview || len(approval.Reviews) == 0 || approval.AlertFailure != nil {
		t.Fatalf("submit = %+v, %v", approval, err)
	}
	id, markCode := approval.OrderExternalID, approval.Reviews[0].Code
	assertOrderReviewAlerts(t, service, ctx, id, markCode)
	for _, raw := range []string{`null`, `[]`, `{"order":"other"}`} {
		if _, err := service.Resolve(ctx, id, "close", json.RawMessage(raw)); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("invalid close input %s: %v", raw, err)
		}
		assertOrderReviewAlerts(t, service, ctx, id, markCode)
	}
	if _, err := service.Resolve(ctx, id, "close", json.RawMessage(`{}`)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("close waiting order = %v", err)
	}
	assertOrderReviewAlerts(t, service, ctx, id, markCode)
}

func TestOrderReviewAlertCloseMissingOrder(t *testing.T) {
	cp, service, ctx := orderReviewAlertApp(t)
	const id = "missing-order"
	if _, err := cp.GetOrder(ctx, id); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing order = %v", err)
	}
	event := alerts.Event{
		ID: id, Type: orderAwaitingReviewAlert, At: time.Now().UTC(), Source: domain.SourceAPI,
		Payload: json.RawMessage(`{"order":"missing-order","account":"other","markCode":"order_size_limit"}`),
	}
	if err := service.Publish(ctx, event); err != nil {
		t.Fatal(err)
	}
	assertOrderReviewAlerts(t, service, ctx, id, "order_size_limit")
	resolution, err := service.Resolve(ctx, id, "close", json.RawMessage(`{}`))
	if err != nil || resolution.Actor != domain.PrincipalOperator || resolution.Source != domain.SourceAPI || string(resolution.Result) != `{"closed":true}` {
		t.Fatalf("close missing order = %+v, %v", resolution, err)
	}
	assertOrderReviewAlerts(t, service, ctx, "", "")
	assertOrderReviewResolution(t, cp, ctx, id, domain.Caller{Source: domain.SourceAPI, Principal: domain.PrincipalOperator})
	if _, err := service.Resolve(ctx, id, "close", json.RawMessage(`{}`)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("close missing open alert = %v", err)
	}
}

func TestOrderReviewAlertVisibility(t *testing.T) {
	for _, mode := range []string{accountmode.ModeMulti, accountmode.ModeSingle} {
		t.Run(mode, func(t *testing.T) {
			var service *alerts.Service
			cfg := orderReviewAlertConfig(t, &service)
			cfg.InitialSetup.AccountMode = mode
			settings := map[string]string{}
			if mode == accountmode.ModeSingle {
				settings[accountmode.SettingKeyMode] = "single:default"
			}
			serveSeededApp(t, cfg, settings)
			ctx := auth.ContextWithCaller(t.Context(), domain.Caller{Source: domain.SourceAPI, Principal: domain.PrincipalOperator})
			event := alerts.Event{
				ID: "hidden-order", Type: orderAwaitingReviewAlert, At: time.Now().UTC(), Source: domain.SourceAPI,
				Payload: json.RawMessage(`{"order":"hidden-order","account":"other","markCode":"order_size_limit"}`),
			}
			if err := service.Publish(ctx, event); err != nil {
				t.Fatal(err)
			}
			reader := auth.ContextWithCaller(t.Context(), domain.Caller{Source: domain.SourceMCP, Principal: domain.PrincipalOperator})
			if mode == accountmode.ModeMulti {
				assertOrderReviewAlerts(t, service, reader, event.ID, "order_size_limit")
				return
			}
			assertOrderReviewAlerts(t, service, reader, "", "")
			if _, err := service.Detail(reader, event.ID); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("hidden detail = %v", err)
			}
			if _, err := service.Resolve(reader, event.ID, "close", json.RawMessage(`{}`)); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("hidden close = %v", err)
			}
		})
	}
}

type orderReviewAlertBarrierNode struct {
	node.Node
	entered chan<- domain.Order
	release <-chan struct{}
}

func (n orderReviewAlertBarrierNode) InstallOrderReviewObserver(observer node.OrderReviewObserver) error {
	return n.Node.(node.OrderReviewObserverHost).InstallOrderReviewObserver(orderReviewAlertBarrier{
		OrderReviewObserver: observer, entered: n.entered, release: n.release,
	})
}

type orderReviewAlertBarrier struct {
	node.OrderReviewObserver
	entered chan<- domain.Order
	release <-chan struct{}
}

func (o orderReviewAlertBarrier) OrderAwaitingReview(ctx context.Context, order domain.Order, mark domain.ReviewMark, caller domain.Caller) error {
	o.entered <- order
	<-o.release
	return o.OrderReviewObserver.OrderAwaitingReview(ctx, order, mark, caller)
}

func TestOrderReviewAlertDecisionBeforePublication(t *testing.T) {
	var service *alerts.Service
	builder := frameworkapp.NewBuilder()
	if err := Register(builder, orderReviewAlertConfig(t, &service)); err != nil {
		t.Fatal(err)
	}
	entered := make(chan domain.Order, 1)
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	builder.SetNodeBuilder(func(ctx context.Context, st store.Store, fatal frameworkapp.FatalShutdownHook) (node.Node, fwengine.Engine, error) {
		n, eng, err := node.NewLocalNode(ctx, domain.DefaultRealm, st, engine.NewOpenPitEngineBuildFunc(), fatal)
		if err != nil {
			return nil, eng, err
		}
		return orderReviewAlertBarrierNode{Node: n, entered: entered, release: release}, eng, nil
	})
	app, err := builder.Build(t.Context(), slog.Default(), failOnFatal(t))
	if err != nil {
		t.Fatal(err)
	}
	var submitDone, decisionDone chan struct{}
	t.Cleanup(func() {
		unblock()
		for _, done := range []<-chan struct{}{submitDone, decisionDone} {
			if done == nil {
				continue
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("order review operation did not finish during cleanup")
			}
		}
		if err := app.Close(); err != nil {
			t.Fatal(err)
		}
	})
	cp := app.Service()
	ctx := auth.ContextWithCaller(t.Context(), domain.Caller{Source: domain.SourceAPI, Principal: domain.PrincipalOperator})
	if _, err := cp.CreateAccount(ctx, domain.Account{Code: "other"}); err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"AAPL", "USD"} {
		if _, err := cp.CreateAsset(ctx, domain.Asset{Code: code}); err != nil {
			t.Fatal(err)
		}
	}
	order := prepareOrderReviewAlert(t, cp, ctx, domain.RuleModeReviewWithoutHold)
	type submitted struct {
		approval backend.SignedApproval
		err      error
	}
	finished := make(chan submitted, 1)
	submitDone = make(chan struct{})
	go func() {
		defer close(submitDone)
		approval, err := cp.SubmitOrder(ctx, order, domain.SubmitModeHold, domain.MissingAccountReject)
		finished <- submitted{approval: approval, err: err}
	}()
	var waiting domain.Order
	select {
	case waiting = <-entered:
	case result := <-finished:
		t.Fatalf("submit finished without reaching publication: %+v, %v", result.approval, result.err)
	case <-time.After(3 * time.Second):
		t.Fatal("submit did not reach publication")
	}
	id := waiting.ExternalID.String()
	assertOrderReviewAlerts(t, service, ctx, "", "")
	caller := domain.Caller{Source: domain.SourcePanel, Principal: domain.PrincipalOperator}
	deciding := auth.ContextWithCaller(t.Context(), caller)
	var decided domain.Order
	var failure *backend.AlertFailure
	var decisionErr error
	decisionDone = make(chan struct{})
	go func() {
		defer close(decisionDone)
		decided, _, failure, decisionErr = cp.CancelOrderReview(deciding, id)
	}()
	select {
	case <-decisionDone:
	case <-time.After(3 * time.Second):
		t.Fatal("publication still holds the account lane during cancel")
	}
	if decisionErr != nil || decided.Status != domain.OrderStatusCancelled || failure == nil || failure.Step != "close" || failure.Message == "" {
		t.Fatalf("decision before publication = %+v, failure %+v, %v", decided, failure, decisionErr)
	}
	stored, err := cp.GetOrder(deciding, id)
	if err != nil || stored.Order.Status != domain.OrderStatusCancelled {
		t.Fatalf("committed decision = %+v, %v", stored, err)
	}
	assertOrderReviewAlerts(t, service, deciding, "", "")
	unblock()
	var result submitted
	select {
	case result = <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("submit did not finish after publication was released")
	}
	if result.err != nil || result.approval.AlertFailure != nil || result.approval.Verdict != domain.VerdictReview || len(result.approval.Reviews) == 0 {
		t.Fatalf("delayed publication = %+v, %v", result.approval, result.err)
	}
	assertOrderReviewAlerts(t, service, deciding, id, result.approval.Reviews[0].Code)
	resolution, err := service.Resolve(deciding, id, "close", json.RawMessage(`{}`))
	if err != nil || resolution.Actor != caller.Principal || resolution.Source != caller.Source || string(resolution.Result) != `{"closed":true}` {
		t.Fatalf("operator close after race = %+v, %v", resolution, err)
	}
	assertOrderReviewAlerts(t, service, deciding, "", "")
	assertOrderReviewResolution(t, cp, deciding, id, caller)
}

func TestOrderReviewAlertPayloadValidation(t *testing.T) {
	_, service, ctx := orderReviewAlertApp(t)
	for i, raw := range []string{
		`null`, `{}`, `{"order":"id","account":"other"}`, `{"account":"other","markCode":"limit"}`,
		`{"order":"id","markCode":"limit"}`, `{"order":null,"account":"other","markCode":"limit"}`,
		`{"order":"","account":"other","markCode":"limit"}`, `{"order":"id","account":"","markCode":"limit"}`,
		`{"order":"id","account":"other","markCode":null}`, `{"order":"id","account":"other","markCode":""}`,
	} {
		event := alerts.Event{
			ID: fmt.Sprintf("invalid-payload-%d", i), Type: orderAwaitingReviewAlert, At: time.Now().UTC(), Source: domain.SourceAPI,
			Payload: json.RawMessage(raw),
		}
		if err := service.Publish(ctx, event); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("invalid payload %s: %v", raw, err)
		}
		assertOrderReviewAlerts(t, service, ctx, "", "")
	}
	event := alerts.Event{
		ID: "id", Type: orderAwaitingReviewAlert, At: time.Now().UTC(), Source: domain.SourceAPI,
		Payload: json.RawMessage(`{"order":"id","account":"other","markCode":"limit"}`),
	}
	if err := service.Publish(ctx, event); err != nil {
		t.Fatal(err)
	}
	assertOrderReviewAlerts(t, service, ctx, event.ID, "limit")
}
