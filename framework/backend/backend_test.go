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
	"slices"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/node"
)

func testLockSettlementPrice([]byte, domain.Order) (string, error) {
	return "", nil
}

func TestNewRejectsNilLockSettlementPrice(t *testing.T) {
	t.Parallel()
	svc, err := New(&limitTestNode{}, nil, nil, nil)
	if err == nil || svc != nil {
		t.Fatalf("New(nil decoder) returned service=%t err=%v, want an error", svc != nil, err)
	}
	if !strings.Contains(err.Error(), "lock settlement") {
		t.Fatalf("New(nil decoder) error = %v, want it to name the lock decoder", err)
	}
}

func TestNewRejectsNilNode(t *testing.T) {
	t.Parallel()
	svc, err := New(nil, nil, nil, testLockSettlementPrice)
	if err == nil || svc != nil {
		t.Fatalf("New(nil node) returned service=%t err=%v, want an error", svc != nil, err)
	}
	if !strings.Contains(err.Error(), "nil node") {
		t.Fatalf("New(nil node) error = %v, want it to name the node", err)
	}
}

type serviceInfoTestNode struct {
	node.Node
	health node.Health
	err    error
}

func (n *serviceInfoTestNode) Health(context.Context) (node.Health, error) {
	return n.health, n.err
}

func TestServiceInfoSeparatesOfficerReleaseFromEngineProfile(t *testing.T) {
	t.Parallel()
	for _, profile := range []string{
		"release",
		"debug",
		"version=0.10.0;profile=release;opt_level=3;debug_assertions=false",
		"version=0.10.0;profile=debug;opt_level=0;debug_assertions=true",
	} {
		t.Run(profile, func(t *testing.T) {
			t.Parallel()
			n := &serviceInfoTestNode{health: node.Health{
				Engine: engine.Health{Version: "0.10.0", BuildProfile: profile},
			}}
			svc, err := New(n, nil, nil, testLockSettlementPrice)
			if err != nil {
				t.Fatal(err)
			}
			info, err := svc.ServiceInfo(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if info.Release != (releaseTag != "") {
				t.Fatalf("Release = %t for Officer tag %q and SDK profile %q",
					info.Release, releaseTag, profile)
			}
			if info.EngineVersion != "0.10.0" || info.EngineBuildProfile != profile {
				t.Fatalf("SDK metadata changed: %+v", info)
			}
		})
	}
}

func TestServiceInfoPropagatesHealthFailure(t *testing.T) {
	t.Parallel()
	want := errors.New("health unavailable")
	svc, err := New(&serviceInfoTestNode{err: want}, nil, nil,
		testLockSettlementPrice)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ServiceInfo(context.Background()); !errors.Is(err, want) {
		t.Fatalf("ServiceInfo error = %v, want %v", err, want)
	}
}

// listTestNode returns fixed rows, in the order given, from each list the
// Service reorders.
type listTestNode struct {
	node.Node

	audit       []domain.AuditRow
	orders      []domain.Order
	trades      []domain.Trade
	adjustments []domain.AccountAdjustmentRecord
}

func (n *listTestNode) ListAudit(context.Context, int) ([]domain.AuditRow, error) {
	return n.audit, nil
}

func (n *listTestNode) ListAuditFiltered(
	context.Context, domain.AuditFilter, int,
) ([]domain.AuditRow, error) {
	return n.audit, nil
}

func (n *listTestNode) ListAllOrders(
	context.Context, domain.AccountID, domain.Source,
) ([]domain.Order, error) {
	return n.orders, nil
}

func (n *listTestNode) ListTrades(
	context.Context, domain.AccountID, domain.Source, int,
) ([]domain.Trade, error) {
	return n.trades, nil
}

func (n *listTestNode) ListAllTrades(
	context.Context, domain.AccountID, domain.Source,
) ([]domain.Trade, error) {
	return n.trades, nil
}

func (n *listTestNode) ListAdjustments(
	context.Context, domain.AccountID, domain.Source, int,
) ([]domain.AccountAdjustmentRecord, error) {
	return n.adjustments, nil
}

// listedRow is the part of a listed record the newest-first ordering reads.
type listedRow struct {
	At         time.Time
	ExternalID domain.ExternalID
}

// TestListsOrderNewestFirst covers every Service list that reorders what the
// node returns. The store orders by the RFC3339Nano text, which is not
// chronological within one second when the fractional precision differs:
// descending text puts the older ".12345Z" above the newer ".123456Z". Rows
// with an identical timestamp are ordered by external id, descending. Every
// input is in an order the result must not keep, so a list that skips the
// sort fails both cases.
func TestListsOrderNewestFirst(t *testing.T) {
	t.Parallel()
	older, err := time.Parse(time.RFC3339Nano, "2026-09-15T10:20:30.12345Z")
	if err != nil {
		t.Fatalf("parse older: %v", err)
	}
	newer, err := time.Parse(time.RFC3339Nano, "2026-09-15T10:20:30.123456Z")
	if err != nil {
		t.Fatalf("parse newer: %v", err)
	}
	scenarios := []struct {
		name string
		in   []listedRow
		want []listedRow
	}{
		{
			// The older row carries the greater id, so ordering by id alone
			// fails as well.
			name: "same_second_precision",
			in:   []listedRow{{older, "id-2"}, {newer, "id-1"}},
			want: []listedRow{{newer, "id-1"}, {older, "id-2"}},
		},
		{
			name: "equal_time_external_id_desc",
			in:   []listedRow{{older, "id-1"}, {older, "id-2"}},
			want: []listedRow{{older, "id-2"}, {older, "id-1"}},
		},
	}
	methods := []struct {
		name string
		list func(context.Context, *Service) ([]listedRow, error)
	}{
		{
			name: "ListAudit",
			list: func(ctx context.Context, svc *Service) ([]listedRow, error) {
				rows, err := svc.ListAudit(ctx, 10)
				got := make([]listedRow, len(rows))
				for i, row := range rows {
					got[i] = listedRow{row.At, row.ExternalID}
				}
				return got, err
			},
		},
		{
			name: "ListAuditFiltered",
			list: func(ctx context.Context, svc *Service) ([]listedRow, error) {
				rows, err := svc.ListAuditFiltered(ctx, domain.AuditFilter{}, 10)
				got := make([]listedRow, len(rows))
				for i, row := range rows {
					got[i] = listedRow{row.At, row.ExternalID}
				}
				return got, err
			},
		},
		{
			name: "ListTrades",
			list: func(ctx context.Context, svc *Service) ([]listedRow, error) {
				trades, err := svc.ListTrades(ctx, "", "", 10)
				got := make([]listedRow, len(trades))
				for i, trade := range trades {
					got[i] = listedRow{trade.At, trade.ExternalID}
				}
				return got, err
			},
		},
		{
			name: "ListAllOrders",
			list: func(ctx context.Context, svc *Service) ([]listedRow, error) {
				orders, err := svc.ListAllOrders(ctx, "", "")
				got := make([]listedRow, len(orders))
				for i, order := range orders {
					got[i] = listedRow{order.At, order.ExternalID}
				}
				return got, err
			},
		},
		{
			name: "ListAllTrades",
			list: func(ctx context.Context, svc *Service) ([]listedRow, error) {
				trades, err := svc.ListAllTrades(ctx, "", "")
				got := make([]listedRow, len(trades))
				for i, trade := range trades {
					got[i] = listedRow{trade.At, trade.ExternalID}
				}
				return got, err
			},
		},
		{
			// An empty account takes the cross-account path, which sorts.
			name: "ListAllAdjustments",
			list: func(ctx context.Context, svc *Service) ([]listedRow, error) {
				recs, err := svc.ListAllAdjustments(ctx, "", "", 10)
				got := make([]listedRow, len(recs))
				for i, rec := range recs {
					got[i] = listedRow{rec.At, rec.ExternalID}
				}
				return got, err
			},
		},
	}
	for _, method := range methods {
		t.Run(method.name, func(t *testing.T) {
			t.Parallel()
			for _, scenario := range scenarios {
				t.Run(scenario.name, func(t *testing.T) {
					t.Parallel()
					n := &listTestNode{}
					for _, r := range scenario.in {
						n.audit = append(n.audit, domain.AuditRow{At: r.At, ExternalID: r.ExternalID})
						n.orders = append(n.orders, domain.Order{At: r.At, ExternalID: r.ExternalID})
						n.trades = append(n.trades, domain.Trade{At: r.At, ExternalID: r.ExternalID})
						n.adjustments = append(n.adjustments, domain.AccountAdjustmentRecord{
							At: r.At, ExternalID: r.ExternalID,
						})
					}
					got, err := method.list(systemCtx(), &Service{node: n, scope: EveryAccount{}})
					if err != nil {
						t.Fatalf("%s: %v", method.name, err)
					}
					if !slices.EqualFunc(got, scenario.want, func(a, b listedRow) bool {
						return a.At.Equal(b.At) && a.ExternalID == b.ExternalID
					}) {
						t.Fatalf("%s order = %v, want %v", method.name, got, scenario.want)
					}
				})
			}
		})
	}
}
