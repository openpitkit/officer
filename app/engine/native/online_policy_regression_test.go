// Copyright The Pit Project Owners. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

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

package native

import (
	"context"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/internal/store/sqlite"
)

func TestLocalNode_BrokerBarrierRemovalKeepsNativeEngineAndSink(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(t.TempDir() + "/officer.db")
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	builds := 0
	n, _, err := node.NewLocalNode(
		ctx,
		store,
		func(snapshot engine.Snapshot) (engine.Engine, error) {
			builds++
			return BuildOpenPitEngine("", snapshot)
		},
	)
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	caller := domain.Caller{
		Source:    domain.SourcePanel,
		Principal: domain.PrincipalOperator,
	}

	if sink, err := n.PutRateLimit(ctx, domain.LimitRate{
		Scope: domain.ScopeBroker, MaxOrders: 100, Window: time.Second,
	}, caller); err != nil || sink == nil {
		t.Fatalf("PutRateLimit first barrier = (sink %T, %v), want one SDK-required rebuild", sink, err)
	}
	if sink, err := n.PutRateLimit(ctx, domain.LimitRate{
		Scope: domain.ScopeAsset, Asset: "USD", MaxOrders: 50, Window: time.Second,
	}, caller); err != nil || sink != nil {
		t.Fatalf("PutRateLimit asset = (sink %T, %v), want online success", sink, err)
	}
	sinkBefore := n.CurrentMarketDataSink()
	buildsBefore := builds
	if sink, err := n.DeleteLimit(ctx, node.LimitTarget{
		Policy: domain.PolicyRateLimit, Scope: domain.ScopeBroker,
	}, caller); err != nil || sink != nil {
		t.Fatalf("DeleteLimit rate broker = (sink %T, %v), want online success", sink, err)
	}
	if builds != buildsBefore || n.CurrentMarketDataSink() != sinkBefore {
		t.Fatal("rate broker removal rebuilt engine or replaced market-data sink")
	}

	if sink, err := n.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
		Scope: domain.ScopeBroker, MaxQuantity: "100",
	}, caller); err != nil || sink == nil {
		t.Fatalf("PutOrderSizeLimit first barrier = (sink %T, %v), want one SDK-required rebuild", sink, err)
	}
	if sink, err := n.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
		Scope: domain.ScopeAsset, Asset: "USD", MaxQuantity: "50",
	}, caller); err != nil || sink != nil {
		t.Fatalf("PutOrderSizeLimit asset = (sink %T, %v), want online success", sink, err)
	}
	sinkBefore = n.CurrentMarketDataSink()
	buildsBefore = builds
	if sink, err := n.DeleteLimit(ctx, node.LimitTarget{
		Policy: domain.PolicyOrderSizeLimit, Scope: domain.ScopeBroker,
	}, caller); err != nil || sink != nil {
		t.Fatalf("DeleteLimit order-size broker = (sink %T, %v), want online success", sink, err)
	}
	if builds != buildsBefore || n.CurrentMarketDataSink() != sinkBefore {
		t.Fatal("order-size broker removal rebuilt engine or replaced market-data sink")
	}
}
