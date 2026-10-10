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

package backend

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/node"
)

type venueTestNode struct {
	node.Node
	detail   domain.OrderDetail
	venue    *domain.VenueOrder
	venueErr error
	reports  int
	caller   domain.Caller
}

func (n *venueTestNode) GetOrder(context.Context, domain.ExternalID) (domain.OrderDetail, error) {
	return n.detail, nil
}
func (n *venueTestNode) GetVenueOrder(context.Context, domain.ExternalID) (domain.VenueOrder, bool, error) {
	if n.venue == nil {
		return domain.VenueOrder{}, false, n.venueErr
	}
	return *n.venue, true, n.venueErr
}
func (n *venueTestNode) ApplyExecutionReport(_ context.Context, in domain.ExecutionReportInput, caller domain.Caller) (engine.ExecutionReportResult, error) {
	if caller.Source != domain.SourceSystem && n.venue != nil {
		return engine.ExecutionReportResult{}, domain.ErrVenueOrderManaged
	}
	n.reports++
	n.caller = caller
	n.detail.Order.Status = in.OrderStatus
	n.detail.Events = append(n.detail.Events, domain.OrderEvent{Type: domain.OrderEventFill, Payload: domain.OrderEventPayload{FillQuantity: in.FillQuantity, FillPrice: in.FillPrice}})
	n.detail.Trades = append(n.detail.Trades, domain.Trade{Quantity: in.FillQuantity, Price: in.FillPrice})
	return engine.ExecutionReportResult{}, nil
}

func TestGetOrderVenue(t *testing.T) {
	for _, linked := range []bool{false, true} {
		n := &venueTestNode{detail: domain.OrderDetail{Order: domain.Order{ExternalID: "order-1"}}}
		link := domain.VenueOrder{Order: "order-1", Connection: "connection-1", VenueOrderID: "venue-order-1"}
		if linked {
			n.venue = &link
		}
		s := &Service{node: n, scope: EveryAccount{}}
		got, err := s.GetOrder(systemCtx(), "order-1")
		if err != nil {
			t.Fatal(err)
		}
		if linked && (got.Venue == nil || *got.Venue != link) {
			t.Fatal("venue link was not filled")
		}
		if !linked && got.Venue != nil {
			t.Fatal("unlinked order gained a venue")
		}
		got.Venue = nil
		if !reflect.DeepEqual(got, n.detail) {
			t.Fatal("order detail changed beyond venue link")
		}
		n.venueErr = domain.ErrConflict
		if _, err := s.GetOrder(systemCtx(), "order-1"); !errors.Is(err, domain.ErrConflict) {
			t.Fatal("venue read failure was ignored")
		}
	}
}

func TestVenueManagedOrders(t *testing.T) {
	manual := auth.ContextWithCaller(context.Background(), domain.Caller{Source: domain.SourceAPI, Principal: "operator"})
	for _, claimed := range []bool{false, true} {
		for _, action := range []string{"report", "confirm", "cancel"} {
			t.Run(action+"/claimed="+map[bool]string{false: "false", true: "true"}[claimed], func(t *testing.T) {
				n := &venueTestNode{
					detail: domain.OrderDetail{Order: domain.Order{ExternalID: "order-1", DropCopy: true, Status: domain.OrderStatusAccepted}},
					venue:  &domain.VenueOrder{Order: "order-1", Connection: "connection-1"},
				}
				if claimed {
					n.detail.Events = []domain.OrderEvent{{Payload: domain.OrderEventPayload{ExecutionReport: &domain.ExecutionReportRequest{Order: "order-1"}}}}
				}
				before := n.detail
				s := &Service{node: n, scope: EveryAccount{}}
				var err error
				switch action {
				case "report":
					_, _, err = s.ApplyExecutionReport(manual, domain.ExecutionReportInput{Order: "order-1", OrderStatus: domain.OrderStatusFilled, FillQuantity: "1", FillPrice: "100"})
				case "confirm":
					_, _, err = s.ConfirmExecution(manual, "order-1")
				case "cancel":
					_, _, err = s.CancelOrder(manual, "order-1", "1", "routine")
				}
				if !errors.Is(err, domain.ErrVenueOrderManaged) {
					t.Fatalf("%s = %v, want ErrVenueOrderManaged", action, err)
				}
				if n.reports != 0 || !reflect.DeepEqual(n.detail, before) {
					t.Fatal("refused manual action changed order, events or trades")
				}
			})
		}
	}
	for _, tc := range []struct {
		name   string
		linked bool
		caller domain.Caller
	}{
		{"system-linked", true, auth.SystemCaller()},
		{"manual-unlinked", false, domain.Caller{Source: domain.SourceAPI, Principal: "operator"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := &venueTestNode{detail: domain.OrderDetail{Order: domain.Order{ExternalID: "order-1", DropCopy: true, Status: domain.OrderStatusAccepted}}}
			if tc.linked {
				n.venue = &domain.VenueOrder{Order: "order-1"}
			}
			s := &Service{node: n, scope: EveryAccount{}}
			in := domain.ExecutionReportInput{Order: "order-1", OrderStatus: domain.OrderStatusFilled, FillQuantity: "1", FillPrice: "100"}
			_, _, err := s.ApplyExecutionReport(auth.ContextWithCaller(context.Background(), tc.caller), in)
			if err != nil || n.reports != 1 || n.caller != tc.caller || n.detail.Order.Status != domain.OrderStatusFilled || len(n.detail.Events) != 1 || len(n.detail.Trades) != 1 {
				t.Fatalf("report was not applied: %v", err)
			}
		})
	}
}
