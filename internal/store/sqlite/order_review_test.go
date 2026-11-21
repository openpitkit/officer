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

package sqlite

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	fwstore "go.openpit.dev/officer/framework/store"
)

func submitReviewOrder(t *testing.T, ctx context.Context, rs fwstore.RealmStore,
	review domain.OrderReview,
) domain.Order {
	t.Helper()
	order, err := rs.RecordOrderSubmission(ctx, sampleOrder(),
		domain.OrderEvent{Type: domain.OrderEventSubmitted, Source: domain.SourcePanel},
		func(order domain.Order) (domain.OrderSettlement, error) {
			return domain.OrderSettlement{
				OrderStatus: domain.OrderStatusAwaitingReview, Review: &review,
				Events: []domain.OrderEvent{{Type: domain.OrderEventPreTradeReview, Source: domain.SourcePanel}},
			}, nil
		})
	if err != nil {
		t.Fatalf("submit review order: %v", err)
	}
	return order
}

func TestOrderReviewSettlement(t *testing.T) {
	for _, trading := range []bool{false, true} {
		name := "gateway"
		if trading {
			name = "trading"
		}
		t.Run(name, func(t *testing.T) {
			ctx, rs, connection := seedTradingFixtures(t)
			review := domain.OrderReview{Mode: "hold"}
			if trading {
				review.Mode = "immediate"
				review.Destination = &domain.TradingDestination{
					Connection: connection.ExternalID, VenueAccount: "paper-account", Route: `{}`,
				}
			}
			for _, target := range []domain.OrderStatus{domain.OrderStatusCommitted, domain.OrderStatusCancelled} {
				t.Run(string(target), func(t *testing.T) {
					order := submitReviewOrder(t, ctx, rs, review)
					detail, err := rs.GetOrder(ctx, order.ExternalID)
					if err != nil || !reflect.DeepEqual(detail.Review, &review) || detail.Order.Status != domain.OrderStatusAwaitingReview {
						t.Fatalf("waiting order = %+v, err=%v", detail, err)
					}
					if _, err := rs.RecordOrderSettlement(ctx, domain.OrderSettlement{
						Order: order.ExternalID, Account: order.Account, OrderStatus: domain.OrderStatusAwaitingReview,
						Events: []domain.OrderEvent{{Type: domain.OrderEventPreTradeRejected, Source: domain.SourcePanel}},
					}); err != nil {
						t.Fatalf("retry review: %v", err)
					}
					detail, err = rs.GetOrder(ctx, order.ExternalID)
					if err != nil || !reflect.DeepEqual(detail.Review, &review) || len(detail.Events) != 3 {
						t.Fatalf("retry did not retain review and append event: %+v err=%v", detail, err)
					}
					if _, err := rs.RecordOrderSettlement(ctx, domain.OrderSettlement{
						Order: order.ExternalID, Account: order.Account, OrderStatus: target,
						AllowedFrom: []domain.OrderStatus{domain.OrderStatusAwaitingReview}, ClearReview: true,
					}); err != nil {
						t.Fatalf("leave review: %v", err)
					}
					detail, err = rs.GetOrder(ctx, order.ExternalID)
					if err != nil || detail.Review != nil || detail.Order.Status != target {
						t.Fatalf("settled order = %+v err=%v", detail, err)
					}
				})
			}
		})
	}
}

func TestOrderReviewHeldRoundTrip(t *testing.T) {
	for _, held := range []bool{false, true} {
		t.Run(fmt.Sprintf("held=%t", held), func(t *testing.T) {
			ctx, rs := seedOrderFixtures(t)
			review := domain.OrderReview{Mode: "hold", Held: held}
			order := submitReviewOrder(t, ctx, rs, review)
			detail, err := rs.GetOrder(ctx, order.ExternalID)
			if err != nil || !reflect.DeepEqual(detail.Review, &review) {
				t.Fatalf("stored review=%+v want=%+v err=%v", detail.Review, review, err)
			}
		})
	}
}

func TestOrderReviewInvalidSettlementRollsBack(t *testing.T) {
	for _, tc := range []struct {
		name    string
		waiting bool
		status  domain.OrderStatus
		review  *domain.OrderReview
	}{
		{name: "enter without row", status: domain.OrderStatusAwaitingReview},
		{name: "row for committed order", status: domain.OrderStatusCommitted, review: &domain.OrderReview{Mode: "hold"}},
		{name: "leave without clearing", waiting: true, status: domain.OrderStatusCommitted},
		{name: "unknown mode", status: domain.OrderStatusAwaitingReview, review: &domain.OrderReview{Mode: "unknown"}},
		{name: "unknown connection", status: domain.OrderStatusAwaitingReview, review: &domain.OrderReview{
			Mode: "hold", Destination: &domain.TradingDestination{Connection: "missing-connection", Route: `{}`},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, rs := seedOrderFixtures(t)
			var order domain.Order
			if tc.waiting {
				order = submitReviewOrder(t, ctx, rs, domain.OrderReview{Mode: "hold"})
			} else {
				var err error
				order, err = rs.CreateOrder(ctx, sampleOrder())
				if err != nil {
					t.Fatal(err)
				}
			}
			before, err := rs.GetOrder(ctx, order.ExternalID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = rs.RecordOrderSettlement(ctx, domain.OrderSettlement{
				Order: order.ExternalID, Account: order.Account, OrderStatus: tc.status, Review: tc.review,
				Events: []domain.OrderEvent{{Type: domain.OrderEventPreTradeReview, Source: domain.SourcePanel}},
			})
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("invalid settlement error = %v, want ErrInvalid", err)
			}
			after, err := rs.GetOrder(ctx, order.ExternalID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("failed settlement changed order: before=%+v after=%+v err=%v", before, after, err)
			}
		})
	}
}

func TestOrderReviewInvalidDestinationRollsBack(t *testing.T) {
	for _, route := range []string{" {}", "[]"} {
		t.Run(route, func(t *testing.T) {
			ctx, rs, connection := seedTradingFixtures(t)
			order, err := rs.CreateOrder(ctx, sampleOrder())
			if err != nil {
				t.Fatal(err)
			}
			before, err := rs.GetOrder(ctx, order.ExternalID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = rs.RecordOrderSettlement(ctx, domain.OrderSettlement{
				Order: order.ExternalID, Account: order.Account,
				OrderStatus: domain.OrderStatusAwaitingReview,
				Review: &domain.OrderReview{Mode: "hold", Destination: &domain.TradingDestination{
					Connection: connection.ExternalID, VenueAccount: "paper-account", Route: route,
				}},
				Events: []domain.OrderEvent{{Type: domain.OrderEventPreTradeReview, Source: domain.SourcePanel}},
			})
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("invalid destination error = %v, want ErrInvalid", err)
			}
			after, err := rs.GetOrder(ctx, order.ExternalID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("failed settlement changed order: before=%+v after=%+v err=%v", before, after, err)
			}
		})
	}
}

func TestOrderReviewInvalidSubmissionRollsBack(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)
	order := sampleOrder()
	order.ExternalID = "failed-review-submission"
	_, err := rs.RecordOrderSubmission(ctx, order,
		domain.OrderEvent{Type: domain.OrderEventSubmitted, Source: domain.SourcePanel},
		func(domain.Order) (domain.OrderSettlement, error) {
			return domain.OrderSettlement{OrderStatus: domain.OrderStatusAwaitingReview}, nil
		})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("submission error = %v, want ErrInvalid", err)
	}
	if _, err := rs.GetOrder(ctx, order.ExternalID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("failed submission persisted order: %v", err)
	}
	archive, err := rs.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil || len(archive.Data.Orders) != 0 || len(archive.Data.OrderEvents) != 0 || len(archive.Data.OrderReviews) != 0 {
		t.Fatalf("failed submission left history: %+v err=%v", archive.Data, err)
	}
}

func TestOrderReviewDirectStatusWritesRefused(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)
	order := sampleOrder()
	order.Status = domain.OrderStatusAwaitingReview
	if _, err := rs.CreateOrder(ctx, order); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("CreateOrder error = %v", err)
	}
	order, err := rs.CreateOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatal(err)
	}
	before, err := rs.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	if err := rs.UpdateOrderStatus(ctx, order.ExternalID, domain.OrderStatusAwaitingReview); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("UpdateOrderStatus error = %v", err)
	}
	after, err := rs.GetOrder(ctx, order.ExternalID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("direct update changed order: %+v err=%v", after, err)
	}
	waiting := submitReviewOrder(t, ctx, rs, domain.OrderReview{Mode: "hold"})
	if err := rs.UpdateOrderStatus(ctx, waiting.ExternalID, domain.OrderStatusCancelled); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("direct departure from review error = %v", err)
	}
}

func TestOrderReviewActiveOrders(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)
	order := submitReviewOrder(t, ctx, rs, domain.OrderReview{Mode: "hold"})
	if count, err := rs.CountActiveOrders(ctx, domain.AccountRestriction{Account: order.Account}); err != nil || count != 1 {
		t.Fatalf("active review orders = %d, err=%v, want 1", count, err)
	}
	blockers, err := rs.ListAccountsBlockingCurrencyChange(ctx, []domain.AccountID{order.Account})
	if err != nil || len(blockers) != 1 || len(blockers[0].Reasons) != 1 ||
		blockers[0].Reasons[0].Kind != "active_order" || blockers[0].Reasons[0].Status != "awaiting_review" {
		t.Fatalf("review currency blockers = %+v err=%v", blockers, err)
	}
	for _, tc := range []struct {
		name   string
		remove func() error
	}{
		{"account", func() error { return rs.DeleteAccount(ctx, order.Account, false) }},
		{"base asset", func() error { return rs.DeleteAsset(ctx, order.BaseAsset, false) }},
		{"quote asset", func() error { return rs.DeleteAsset(ctx, order.QuoteAsset, false) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var dependents domain.HasDependentsError
			err := tc.remove()
			if !errors.As(err, &dependents) {
				t.Fatalf("delete error = %v, want HasDependentsError", err)
			}
			if len(dependents.Dependents) == 0 {
				t.Fatalf("missing order dependency: %+v", dependents)
			}
			found := false
			for _, dependent := range dependents.Dependents {
				if dependent.Kind == "order_record" && dependent.Count == 1 {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing order_record dependency: %+v", dependents)
			}
		})
	}
}

func TestOrderReviewConnectionDeletion(t *testing.T) {
	ctx, rs, connection := seedTradingFixtures(t)
	if err := rs.SetTradingConnectionEnabled(ctx, connection.ExternalID, false); err != nil {
		t.Fatal(err)
	}
	order := submitReviewOrder(t, ctx, rs, domain.OrderReview{Mode: "hold", Destination: &domain.TradingDestination{
		Connection: connection.ExternalID, VenueAccount: "paper-account", Route: `{}`,
	}})
	err := rs.DeleteTradingConnection(ctx, connection.ExternalID)
	if !errors.Is(err, domain.ErrConflict) || !strings.Contains(err.Error(), "trading connection has order history and cannot be deleted") {
		t.Fatalf("review connection deletion error = %v, want order-history conflict", err)
	}
	if _, err := rs.RecordOrderSettlement(ctx, domain.OrderSettlement{
		Order: order.ExternalID, Account: order.Account, OrderStatus: domain.OrderStatusCancelled, ClearReview: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := rs.DeleteTradingConnection(ctx, connection.ExternalID); err != nil {
		t.Fatalf("delete after review cleared: %v", err)
	}
}

func TestOrderReviewBackupRoundTrip(t *testing.T) {
	ctx, source, connection := seedTradingFixtures(t)
	for _, review := range []domain.OrderReview{
		{Mode: "hold"},
		{Mode: "hold", Held: true},
		{Mode: "immediate", Destination: &domain.TradingDestination{
			Connection: connection.ExternalID, VenueAccount: "paper-account", Route: `{}`,
		}},
		{Mode: "immediate", Held: true, Destination: &domain.TradingDestination{
			Connection: connection.ExternalID, VenueAccount: "paper-account", Route: `{}`,
		}},
	} {
		submitReviewOrder(t, ctx, source, review)
	}
	archive, err := source.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil || len(archive.Data.OrderReviews) != 4 {
		t.Fatalf("export reviews=%+v err=%v", archive.Data.OrderReviews, err)
	}
	for _, archived := range archive.Data.OrderReviews {
		want, err := source.GetOrder(ctx, archived.Order)
		if err != nil {
			t.Fatal(err)
		}
		if archived.Held == nil || *archived.Held != want.Review.Held {
			t.Fatalf("exported held=%v want=%t", archived.Held, want.Review.Held)
		}
	}
	archive = cloneBackupArchive(t, archive)
	for _, mode := range []backup.RestoreMode{backup.RestoreModeReplaceAll, backup.RestoreModeOverwrite, backup.RestoreModeInsertMissing} {
		t.Run(string(mode), func(t *testing.T) {
			_, target := newRealmStore(t, "review-target")
			for range 2 {
				if _, err := target.RestoreBackup(ctx, archive, backup.RestoreOptions{Scope: backup.Scope{All: true}, Mode: mode}); err != nil {
					t.Fatal(err)
				}
				for _, archived := range archive.Data.OrderReviews {
					want, err := source.GetOrder(ctx, archived.Order)
					if err != nil {
						t.Fatal(err)
					}
					got, err := target.GetOrder(ctx, archived.Order)
					if err != nil || !reflect.DeepEqual(got.Review, want.Review) || got.Order.Status != domain.OrderStatusAwaitingReview {
						t.Fatalf("restored order %s review=%+v want=%+v err=%v", archived.Order, got.Review, want.Review, err)
					}
				}
			}
			changed := cloneBackupArchive(t, archive)
			*changed.Data.OrderReviews[0].Held = !*archive.Data.OrderReviews[0].Held
			changed.Data.OrderReviews[0].Mode = "immediate"
			if archive.Data.OrderReviews[0].Mode == "immediate" {
				changed.Data.OrderReviews[0].Mode = "hold"
			}
			if _, err := target.RestoreBackup(ctx, changed, backup.RestoreOptions{Scope: backup.Scope{All: true}, Mode: mode}); err != nil {
				t.Fatal(err)
			}
			got, err := target.GetOrder(ctx, changed.Data.OrderReviews[0].Order)
			wantMode := changed.Data.OrderReviews[0].Mode
			wantHeld := *changed.Data.OrderReviews[0].Held
			if mode == backup.RestoreModeInsertMissing {
				wantMode = archive.Data.OrderReviews[0].Mode
				wantHeld = *archive.Data.OrderReviews[0].Held
			}
			if err != nil || got.Review == nil || got.Review.Mode != wantMode || got.Review.Held != wantHeld {
				t.Fatalf("review after restore=%+v want mode=%s held=%t err=%v", got.Review, wantMode, wantHeld, err)
			}
			cleared := cloneBackupArchive(t, archive)
			cleared.Data.OrderReviews = nil
			for i := range cleared.Data.Orders {
				cleared.Data.Orders[i].Order.Status = domain.OrderStatusCancelled
			}
			if _, err := target.RestoreBackup(ctx, cleared, backup.RestoreOptions{Scope: backup.Scope{All: true}, Mode: mode}); err != nil {
				t.Fatal(err)
			}
			for _, rec := range cleared.Data.Orders {
				got, err := target.GetOrder(ctx, rec.Order.ExternalID)
				if err != nil {
					t.Fatal(err)
				}
				if mode == backup.RestoreModeInsertMissing {
					if got.Review == nil || got.Order.Status != domain.OrderStatusAwaitingReview {
						t.Fatalf("insert-missing overwrote waiting order: %+v", got)
					}
				} else if got.Review != nil || got.Order.Status != domain.OrderStatusCancelled {
					t.Fatalf("overwrite left review row: %+v", got)
				}
			}
		})
	}
}

func TestOrderReviewBackupInvalidRollsBack(t *testing.T) {
	ctx, source := seedOrderFixtures(t)
	submitReviewOrder(t, ctx, source, domain.OrderReview{Mode: "hold"})
	archive, err := source.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*backup.Archive)
	}{
		{"missing row", func(a *backup.Archive) { a.Data.OrderReviews = nil }},
		{"row for nonwaiting order", func(a *backup.Archive) { a.Data.Orders[0].Order.Status = domain.OrderStatusCommitted }},
		{"missing connection", func(a *backup.Archive) {
			a.Data.OrderReviews[0].Connection = "missing-connection"
			a.Data.OrderReviews[0].VenueAccount = "paper-account"
			a.Data.OrderReviews[0].Route = "{}"
		}},
		{"missing order", func(a *backup.Archive) {
			held := false
			a.Data.OrderReviews = append(a.Data.OrderReviews, backup.OrderReview{Order: "target-only-order", Mode: "hold", Held: &held})
		}},
		{"unknown mode", func(a *backup.Archive) { a.Data.OrderReviews[0].Mode = "unknown" }},
	} {
		for _, mode := range []backup.RestoreMode{backup.RestoreModeReplaceAll, backup.RestoreModeOverwrite, backup.RestoreModeInsertMissing} {
			t.Run(tc.name+"/"+string(mode), func(t *testing.T) {
				_, target := seedOrderFixtures(t)
				if tc.name == "missing order" {
					order := sampleOrder()
					order.ExternalID = "target-only-order"
					if _, err := target.CreateOrder(ctx, order); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := target.CreateGroup(ctx, domain.AccountGroup{Code: "keep-me"}); err != nil {
					t.Fatal(err)
				}
				before, err := target.ExportBackup(ctx, backup.Scope{All: true})
				if err != nil {
					t.Fatal(err)
				}
				invalid := cloneBackupArchive(t, archive)
				tc.change(&invalid)
				_, err = target.RestoreBackup(ctx, invalid, backup.RestoreOptions{Scope: backup.Scope{All: true}, Mode: mode})
				if !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), "activity_history") {
					t.Fatalf("invalid restore error=%v, want activity_history ErrInvalid", err)
				}
				after, err := target.ExportBackup(ctx, backup.Scope{All: true})
				if err != nil || !reflect.DeepEqual(before.Data, after.Data) {
					t.Fatalf("failed restore changed realm, err=%v", err)
				}
			})
		}
	}
}

func TestOrderReviewBackupNarrowedTradingPreservesGatewayReview(t *testing.T) {
	ctx, source := seedOrderFixtures(t)
	archive, err := source.ExportBackup(ctx, backup.Scope{
		Sections: []backup.Section{backup.SectionTrading},
	})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	_, target := seedOrderFixtures(t)
	if _, err := target.CreateAccount(ctx, domain.Account{Code: "acc-2"}); err != nil {
		t.Fatal(err)
	}
	order := sampleOrder()
	order.Account = "acc-2"
	order, err = target.CreateOrder(ctx, order)
	if err != nil {
		t.Fatal(err)
	}
	review := domain.OrderReview{Mode: "hold"}
	if _, err := target.RecordOrderSettlement(ctx, domain.OrderSettlement{
		Order: order.ExternalID, Account: order.Account,
		OrderStatus: domain.OrderStatusAwaitingReview, Review: &review,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := target.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{
			Sections: []backup.Section{backup.SectionTrading},
			Accounts: backup.EntitySelector{Accounts: []string{"acc-1"}},
		},
		Mode: backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup(narrowed trading replace_all): %v", err)
	}
	got, err := target.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Order.Status != domain.OrderStatusAwaitingReview || !reflect.DeepEqual(got.Review, &review) {
		t.Fatalf("unselected gateway order: status=%s review=%+v, want awaiting_review with %+v", got.Order.Status, got.Review, review)
	}
}

func TestOrderReviewBackupScope(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)
	first := submitReviewOrder(t, ctx, rs, domain.OrderReview{Mode: "hold"})
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-2"}); err != nil {
		t.Fatal(err)
	}
	second, err := rs.CreateOrder(ctx, func() domain.Order { o := sampleOrder(); o.Account = "acc-2"; return o }())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rs.RecordOrderSettlement(ctx, domain.OrderSettlement{
		Order: second.ExternalID, Account: second.Account, OrderStatus: domain.OrderStatusAwaitingReview, Review: &domain.OrderReview{Mode: "hold"},
	}); err != nil {
		t.Fatal(err)
	}
	archive, err := rs.ExportBackup(ctx, backup.Scope{Sections: []backup.Section{backup.SectionActivityHistory}, Accounts: backup.EntitySelector{Accounts: []string{"acc-1"}}})
	if err != nil || len(archive.Data.OrderReviews) != 1 || archive.Data.OrderReviews[0].Order != first.ExternalID {
		t.Fatalf("filtered reviews=%+v err=%v", archive.Data.OrderReviews, err)
	}
	withoutActivity, err := rs.ExportBackup(ctx, backup.Scope{Sections: []backup.Section{backup.SectionAccountsGroups}})
	if err != nil || len(withoutActivity.Data.OrderReviews) != 0 {
		t.Fatalf("reviews escaped activity section: %+v err=%v", withoutActivity.Data.OrderReviews, err)
	}
}

func TestOrderReviewDictionarySeedsOpen(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)
	order := submitReviewOrder(t, ctx, rs, domain.OrderReview{Mode: "hold"})
	requests := []domain.AttestationRequestType{
		domain.AttestationRequestReviewConfirm,
		domain.AttestationRequestReviewCancel,
		domain.AttestationRequestReviewForce,
	}
	for _, request := range requests {
		event, err := rs.AppendOrderEvent(ctx, domain.OrderEvent{
			Order: order.ExternalID, Type: domain.OrderEventPreTradeReview, Source: domain.SourcePanel,
		})
		if err != nil {
			t.Fatal(err)
		}
		attestation := domain.EventAttestation{
			Alg: "none", RequestType: request, Mode: "hold", IssuedAt: "2026-11-19T10:00:00Z",
		}
		if err := rs.PutEventAttestation(ctx, event.ExternalID, attestation); err != nil {
			t.Fatalf("request %s seed unavailable: %v", request, err)
		}
	}
	for _, entry := range []fwstore.AuditEntry{
		{Action: domain.AuditActionConfirmOrderReview, OrderID: order.ExternalID.String(), Verdict: domain.VerdictAccept, Source: domain.SourcePanel},
		{Action: domain.AuditActionCancelOrderReview, Source: domain.SourcePanel},
		{Action: domain.AuditActionForceOrderReview, Source: domain.SourcePanel},
	} {
		if err := rs.AppendAudit(ctx, entry); err != nil {
			t.Fatalf("action %s seed unavailable: %v", entry.Action, err)
		}
	}
	archive, err := rs.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil || len(archive.Data.Audit) != 3 {
		t.Fatalf("audit seeds read-back=%+v err=%v", archive.Data.Audit, err)
	}
	detail, err := rs.GetOrder(ctx, order.ExternalID)
	if err != nil || len(detail.Events) != 5 {
		t.Fatalf("event seeds read-back=%+v err=%v", detail.Events, err)
	}
	requestOffset := len(detail.Events) - len(requests)
	for i, request := range requests {
		event := detail.Events[requestOffset+i]
		if event.Attestation == nil || event.Attestation.RequestType != request {
			t.Fatalf("request read-back=%+v want=%s", event, request)
		}
	}
}
