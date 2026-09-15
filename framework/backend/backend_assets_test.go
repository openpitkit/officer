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
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
)

type assetUpdateTestNode struct {
	node.Node

	updated      domain.Asset
	updateErr    error
	updateCalls  int
	engineHandle int
}

func (n *assetUpdateTestNode) UpdateAsset(
	_ context.Context,
	oldCode string,
	_ domain.Asset,
	_ domain.Caller,
) (domain.Asset, error) {
	n.updateCalls++
	if n.updateErr != nil {
		return domain.Asset{}, n.updateErr
	}
	return n.updated, nil
}

type assetUpdateTestMarketDataRuntime struct {
	MarketDataRuntime

	restartErr   error
	restarts     int
	restartAfter func()
}

func (m *assetUpdateTestMarketDataRuntime) Restart() error {
	m.restarts++
	if m.restartAfter != nil {
		m.restartAfter()
	}
	return m.restartErr
}

func newAssetUpdateTestService(
	t *testing.T,
	n *assetUpdateTestNode,
	md MarketDataRuntime,
) *Service {
	t.Helper()
	return &Service{node: n, md: md}
}

func assetUpdateTestAsset(code string) domain.Asset {
	return domain.Asset{
		Code:       code,
		Title:      "US Dollar",
		AssetClass: "currency",
	}
}

func TestServiceUpdateAssetRenameDoesNotRestartMarketData(t *testing.T) {
	t.Parallel()

	n := &assetUpdateTestNode{updated: assetUpdateTestAsset("USDX")}
	md := &assetUpdateTestMarketDataRuntime{}
	svc := newAssetUpdateTestService(t, n, md)

	updated, err := svc.UpdateAsset(
		systemCtx(), "USD", assetUpdateTestAsset("USDX"),
	)
	if err != nil {
		t.Fatalf("UpdateAsset: %v", err)
	}
	if updated.Code != "USDX" {
		t.Fatalf("updated code = %q, want USDX", updated.Code)
	}
	if n.updateCalls != 1 || n.engineHandle != 0 {
		t.Fatalf("node update calls=%d engine handle=%d, want unchanged handle", n.updateCalls, n.engineHandle)
	}
	if md.restarts != 0 {
		t.Fatalf("market-data restarts = %d, want 0", md.restarts)
	}
}

func TestServiceUpdateAssetSameCodeDoesNotRestartMarketData(t *testing.T) {
	t.Parallel()

	n := &assetUpdateTestNode{updated: assetUpdateTestAsset("USD")}
	md := &assetUpdateTestMarketDataRuntime{}
	svc := newAssetUpdateTestService(t, n, md)

	updated, err := svc.UpdateAsset(
		systemCtx(), "USD", assetUpdateTestAsset("USD"),
	)
	if err != nil {
		t.Fatalf("UpdateAsset: %v", err)
	}
	if updated.Code != "USD" {
		t.Fatalf("updated code = %q, want USD", updated.Code)
	}
	if md.restarts != 0 {
		t.Fatalf("market-data restarts = %d, want 0", md.restarts)
	}
	if n.engineHandle != 0 {
		t.Fatalf("engine handle = %d, want unchanged", n.engineHandle)
	}
}
